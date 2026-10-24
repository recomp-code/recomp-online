// recompvoice: see engine.h. The audio backends are audio_ma.c (miniaudio: CoreAudio, WASAPI) and audio_vpio.c
// (macOS voice processing, for echo cancellation); test/e2e.c drives the engine with no devices at all.
//
// Datagram to the relay: 'v', slot (the relay stamps it), seq (u16 LE), flags, the sender's one-way delay to the
// relay in ms (u8, half its ping), then one 10 ms Opus frame. A talk spurt starts with RV_F_START and ends with
// RV_F_END: silence sends nothing at all.
#include "engine.h"

#define _USE_MATH_DEFINES
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "opus/opus.h"

#ifdef _WIN32
#define WIN32_LEAN_AND_MEAN
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
typedef SOCKET rv_sock;
#define RV_BADSOCK INVALID_SOCKET
#define rv_closesock closesocket
#else
#include <arpa/inet.h>
#include <errno.h>
#include <netdb.h>
#include <netinet/in.h>
#include <pthread.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>
#if defined(__APPLE__)
#include <pthread/qos.h>
#endif
typedef int rv_sock;
#define RV_BADSOCK (-1)
#define rv_closesock close
#endif

#define RV_RATE 48000
#define RV_FRAME 480 // 10 ms
#define RV_HDR 6
#define RV_MAXOPUS 400
#define RV_RING 64          // packets per peer (640 ms)
#define RV_CAPRING 8192     // capture samples (170 ms)
#define RV_MIN_DEPTH 2      // jitter buffer: frames held before playing, at least
#define RV_MAX_DEPTH 8      // and at most (80 ms: beyond that a late packet is a lost one)
#define RV_HANG 30          // frames the gate stays open after the voice (300 ms)
#define RV_PLC_MAX 6        // frames concealed before a silent sender is taken as gone quiet
#define RV_FLOOR_WINS 15    // the gate's noise floor: minimum over 15 windows of 100 ms
#define RV_F_START 1
#define RV_F_END 2

// ---- platform -----------------------------------------------------------------------------

static int64_t rv_now_us(void) {
#ifdef _WIN32
	static LARGE_INTEGER f;
	LARGE_INTEGER c;
	if (!f.QuadPart)
		QueryPerformanceFrequency(&f);
	QueryPerformanceCounter(&c);
	return (int64_t)(c.QuadPart * 1000000.0 / f.QuadPart);
#else
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return (int64_t)t.tv_sec * 1000000 + t.tv_nsec / 1000;
#endif
}

#ifdef _WIN32
typedef CRITICAL_SECTION rv_mutex;
static void rv_mutex_init(rv_mutex *m) { InitializeCriticalSection(m); }
static void rv_mutex_free(rv_mutex *m) { DeleteCriticalSection(m); }
static void rv_lock(rv_mutex *m) { EnterCriticalSection(m); }
static void rv_unlock(rv_mutex *m) { LeaveCriticalSection(m); }
typedef HANDLE rv_sem;
static void rv_sem_init(rv_sem *s) { *s = CreateSemaphoreA(NULL, 0, 1 << 20, NULL); }
static void rv_sem_free(rv_sem *s) { CloseHandle(*s); }
static void rv_sem_post(rv_sem *s) { ReleaseSemaphore(*s, 1, NULL); }
static void rv_sem_wait(rv_sem *s, int ms) { WaitForSingleObject(*s, (DWORD)ms); }
typedef HANDLE rv_thread;
typedef DWORD(WINAPI *rv_thread_fn)(void *);
#define RV_THREAD_RET DWORD WINAPI
static int rv_thread_start(rv_thread *t, rv_thread_fn fn, void *arg) {
	*t = CreateThread(NULL, 0, fn, arg, 0, NULL);
	if (*t)
		SetThreadPriority(*t, THREAD_PRIORITY_HIGHEST);
	return *t ? 0 : -1;
}
static void rv_thread_join(rv_thread t) {
	WaitForSingleObject(t, INFINITE);
	CloseHandle(t);
}
static void rv_sleep_ms(int ms) { Sleep((DWORD)ms); }
static void rv_thread_priority(void) {}
#else
typedef pthread_mutex_t rv_mutex;
static void rv_mutex_init(rv_mutex *m) { pthread_mutex_init(m, NULL); }
static void rv_mutex_free(rv_mutex *m) { pthread_mutex_destroy(m); }
static void rv_lock(rv_mutex *m) { pthread_mutex_lock(m); }
static void rv_unlock(rv_mutex *m) { pthread_mutex_unlock(m); }
// a counting semaphore of a mutex and a condition (macOS has no unnamed POSIX semaphores, and the launcher compiles
// this as Objective-C with ARC, where a dispatch semaphore in a struct is an object)
typedef struct {
	pthread_mutex_t m;
	pthread_cond_t c;
	int n;
} rv_sem;
static void rv_sem_init(rv_sem *s) {
	pthread_mutex_init(&s->m, NULL);
	pthread_cond_init(&s->c, NULL);
	s->n = 0;
}
static void rv_sem_free(rv_sem *s) {
	pthread_cond_destroy(&s->c);
	pthread_mutex_destroy(&s->m);
}
static void rv_sem_post(rv_sem *s) {
	pthread_mutex_lock(&s->m);
	s->n++;
	pthread_cond_signal(&s->c);
	pthread_mutex_unlock(&s->m);
}
static void rv_sem_wait(rv_sem *s, int ms) {
	struct timespec t;
	clock_gettime(CLOCK_REALTIME, &t);
	t.tv_nsec += (long)ms * 1000000;
	t.tv_sec += t.tv_nsec / 1000000000;
	t.tv_nsec %= 1000000000;
	pthread_mutex_lock(&s->m);
	while (s->n == 0 && pthread_cond_timedwait(&s->c, &s->m, &t) == 0)
		;
	if (s->n > 0)
		s->n--;
	pthread_mutex_unlock(&s->m);
}
typedef pthread_t rv_thread;
typedef void *(*rv_thread_fn)(void *);
#define RV_THREAD_RET void *
static int rv_thread_start(rv_thread *t, rv_thread_fn fn, void *arg) {
	pthread_attr_t a;
	pthread_attr_init(&a);
	int r = pthread_create(t, &a, fn, arg);
	pthread_attr_destroy(&a);
	return r;
}
static void rv_thread_join(rv_thread t) { pthread_join(t, NULL); }
static void rv_sleep_ms(int ms) { usleep((useconds_t)ms * 1000); }
static void rv_thread_priority(void) {
#if defined(__APPLE__)
	pthread_set_qos_class_self_np(QOS_CLASS_USER_INTERACTIVE, 0); // ahead of the game's threads, never behind them
#endif
}
#endif

// ---- engine -------------------------------------------------------------------------------

typedef struct {
	uint16_t seq;
	uint8_t flags, valid;
	int len;
	uint8_t data[RV_MAXOPUS];
} rv_pkt;

typedef struct {
	rv_mutex mu; // the receiver thread (insert) against the playback callback (take)
	rv_pkt ring[RV_RING];
	int active, playing, reset_dec;
	uint16_t next, max_seq;
	double jitter_ms; // RFC 3550 interarrival jitter
	int64_t prev_arr_us;
	uint16_t prev_seq;
	int sender_owd; // the sender's one-way delay to the relay, ms
	int over;       // frames in a row the buffer held more than it needs
	// playback thread only
	OpusDecoder *dec;
	int plc_run;
	// stats (written by one thread, read by rv_stats)
	volatile int level, talking, depth, loss_pm;
	double good, bad; // decaying counts of frames played from a packet / recovered or concealed
	// set by rv_peer
	volatile int volume, muted;
} rv_peerst;

typedef struct rv_engine rv_engine;

// audio backends (audio_ma.c, audio_vpio.c), or stubs in test/e2e.c
int rv_audio_open(rv_engine *e, int echo);
void rv_audio_close(rv_engine *e);
void rv_audio_capture(rv_engine *e, int on);
int rv_audio_devices(int capture, char *buf, int len);

struct rv_engine {
	volatile int running;
	int audio; // 0: no devices (test/e2e.c feeds and pulls by hand)
	rv_sock sock;
	char token[33];
	int slot;
	rv_thread sender, receiver;
	rv_sem cap_sem;
	// capture ring: the capture callback writes, the sender reads
	float cap[RV_CAPRING];
	volatile unsigned cap_w, cap_r;
	// sender
	OpusEncoder *enc;
	uint16_t seq;
	int sending, hang, ptt_tail;
	double floor_db, win_min[RV_FLOOR_WINS];
	int win_i, win_n;
	// receiver
	volatile int registered, ping_ms;
	int64_t last_reg_us, last_ping_us;
	rv_peerst peers[RV_SLOTS + 1];
	// playback: the mix of the current frame and how much of it went out
	float mix[RV_FRAME];
	int mix_pos;
	// RV_BEEP: a short tone mixed into the output (feedback for the mute key during the game)
	volatile int beep_left, beep_hz;
	double beep_ph;
	// settings (rv_set)
	volatile int muted, ptt, ptt_down, sense, mic_gain, volume, echo, deafen;
	char dev_cap[256], dev_play[256];
	// stats
	volatile int state, level, sent_on;
	// the backend's own data
	void *backend;
};

static rv_engine g_rv;

// for the backends
const char *rv_engine_device(rv_engine *e, int capture) { return capture ? e->dev_cap : e->dev_play; }
void *rv_engine_backend(rv_engine *e) { return e->backend; }
void rv_engine_set_backend(rv_engine *e, void *b) { e->backend = b; }
int rv_engine_muted(rv_engine *e) { return e->muted; }
static rv_mutex g_api; // rv_start / rv_stop / rv_device
static int g_api_init;

static void rv_engine_defaults(rv_engine *e) {
	e->sense = 50;
	e->mic_gain = 100;
	e->volume = 100;
	e->ping_ms = -1;
	for (int i = 1; i <= RV_SLOTS; i++)
		e->peers[i].volume = 100;
}

static int rv_send(rv_engine *e, const void *b, int n) { return (int)send(e->sock, (const char *)b, n, 0); }

static void rv_register(rv_engine *e) {
	char b[36];
	memcpy(b, "RLV1", 4);
	memcpy(b + 4, e->token, 32);
	rv_send(e, b, 36);
	e->last_reg_us = rv_now_us();
}

static void rv_ping(rv_engine *e) {
	uint8_t b[9];
	int64_t t = rv_now_us();
	b[0] = 'p';
	memcpy(b + 1, &t, 8);
	rv_send(e, b, 9);
	e->last_ping_us = t;
}

static int rv_clampi(int v, int lo, int hi) { return v < lo ? lo : v > hi ? hi : v; }

// ---- capture side ---------------------------------------------------------------------------

// rv_on_capture: called by the backend's capture callback (48 kHz mono float). Only copies: the sender does the rest.
void rv_on_capture(rv_engine *e, const float *in, int n) {
	unsigned w = e->cap_w, r = e->cap_r;
	if ((unsigned)(RV_CAPRING - (w - r)) < (unsigned)n)
		return; // the sender fell behind: this block is lost rather than everything delayed
	for (int i = 0; i < n; i++)
		e->cap[(w + i) & (RV_CAPRING - 1)] = in[i];
	__atomic_store_n(&e->cap_w, w + n, __ATOMIC_RELEASE);
	if (((w + n) / RV_FRAME) != (w / RV_FRAME) || n >= RV_FRAME)
		rv_sem_post(&e->cap_sem);
}

// rv_frame_out: one captured 10 ms frame through gain, the gate and Opus, and out to the relay.
static void rv_frame_out(rv_engine *e, float *pcm) {
	float g = e->mic_gain / 100.0f;
	double sum = 0;
	for (int i = 0; i < RV_FRAME; i++) {
		pcm[i] *= g;
		sum += (double)pcm[i] * pcm[i];
	}
	double db = 10 * log10(sum / RV_FRAME + 1e-12);
	e->level = rv_clampi((int)((db + 60) * 100 / 60), 0, 100);

	// The gate: the noise floor is the quietest frame of the last 1.5 s (minimum statistics). A steady fan or hum
	// sets it within that time; a voice cannot, it always pauses between syllables. The voice must stand clear of
	// the floor and of an absolute minimum, both set by RV_SENSE.
	if (db < e->win_min[e->win_i])
		e->win_min[e->win_i] = db;
	if (++e->win_n == 10) { // a new 100 ms window
		e->win_n = 0;
		e->win_i = (e->win_i + 1) % RV_FLOOR_WINS;
		e->win_min[e->win_i] = 0;
	}
	e->floor_db = 0;
	for (int i = 0; i < RV_FLOOR_WINS; i++)
		if (e->win_min[i] < e->floor_db)
			e->floor_db = e->win_min[i];
	if (e->floor_db < -90)
		e->floor_db = -90;
	double margin = 18 - e->sense * 0.12, minimum = -35 - e->sense * 0.3;
	int voice = db > e->floor_db + margin && db > minimum;
	if (voice)
		e->hang = RV_HANG;
	else if (e->hang > 0)
		e->hang--;

	int want;
	if (e->muted)
		want = 0;
	else if (e->ptt) {
		if (e->ptt_down)
			e->ptt_tail = 15; // the end of the last word after the key is let go
		else if (e->ptt_tail > 0)
			e->ptt_tail--;
		want = e->ptt_down || e->ptt_tail > 0;
	} else
		want = voice || e->hang > 0;

	if (!want && !e->sending)
		return;
	uint8_t pkt[RV_HDR + RV_MAXOPUS];
	uint8_t flags = 0;
	if (want && !e->sending) {
		flags = RV_F_START;
		for (int i = 0; i < RV_FRAME; i++)
			pcm[i] *= (float)i / RV_FRAME; // fade in: no click when the gate opens
	} else if (!want) {
		flags = RV_F_END;
		for (int i = 0; i < RV_FRAME; i++)
			pcm[i] *= 1.0f - (float)i / RV_FRAME;
	}
	int n = opus_encode_float(e->enc, pcm, RV_FRAME, pkt + RV_HDR, RV_MAXOPUS);
	if (n < 0)
		return;
	e->sending = want;
	e->sent_on = want;
	pkt[0] = 'v';
	pkt[1] = 0;
	pkt[2] = (uint8_t)(e->seq & 0xff);
	pkt[3] = (uint8_t)(e->seq >> 8);
	pkt[4] = flags;
	pkt[5] = (uint8_t)(e->ping_ms < 0 ? 0 : rv_clampi(e->ping_ms / 2, 0, 255));
	e->seq++;
	rv_send(e, pkt, RV_HDR + n);
}

static RV_THREAD_RET rv_sender(void *arg) {
	rv_engine *e = arg;
	float pcm[RV_FRAME];
	rv_thread_priority();
	while (e->running) {
		rv_sem_wait(&e->cap_sem, 50);
		for (;;) {
			unsigned w = __atomic_load_n(&e->cap_w, __ATOMIC_ACQUIRE), r = e->cap_r;
			if (w - r < RV_FRAME)
				break;
			for (int i = 0; i < RV_FRAME; i++)
				pcm[i] = e->cap[(r + i) & (RV_CAPRING - 1)];
			__atomic_store_n(&e->cap_r, r + RV_FRAME, __ATOMIC_RELEASE);
			rv_frame_out(e, pcm);
		}
	}
	return 0;
}

// ---- receive side ---------------------------------------------------------------------------

static int rv_target(rv_peerst *p) {
	return rv_clampi((int)ceil((2 * p->jitter_ms + 10) / 10), RV_MIN_DEPTH, RV_MAX_DEPTH);
}

static int rv_buffered(rv_peerst *p) {
	int n = 0;
	for (uint16_t s = p->next; (int16_t)(s - p->max_seq) <= 0 && n < RV_RING; s++)
		n += p->ring[s & (RV_RING - 1)].valid && p->ring[s & (RV_RING - 1)].seq == s;
	return n;
}

static void rv_restart_peer(rv_peerst *p, uint16_t seq) {
	for (int i = 0; i < RV_RING; i++)
		p->ring[i].valid = 0;
	p->next = p->max_seq = seq;
	p->playing = 0;
	p->active = 1;
	p->over = 0;
	p->prev_arr_us = 0;
}

static void rv_insert(rv_engine *e, const uint8_t *b, int n) {
	int slot = b[1] - '0';
	if (slot < 1 || slot > RV_SLOTS || slot == e->slot || n <= RV_HDR || n - RV_HDR > RV_MAXOPUS)
		return;
	rv_peerst *p = &e->peers[slot];
	uint16_t seq = (uint16_t)(b[2] | b[3] << 8);
	uint8_t flags = b[4];
	int64_t now = rv_now_us();
	rv_lock(&p->mu);
	// a new talk spurt starts over, unless it is still being played (its gap was concealed and it goes on)
	if (!p->active || (flags & RV_F_START && (!p->playing || (int16_t)(seq - p->next) < 0))) {
		rv_restart_peer(p, seq);
		p->reset_dec = 1;
	}
	int16_t d = (int16_t)(seq - p->next);
	if (d < 0) { // too late: it was already concealed. The buffer is too short for this network: lengthen it
		p->jitter_ms += 5;
		rv_unlock(&p->mu);
		return;
	}
	if (d >= RV_RING) { // far ahead (a long gap): start over from it
		rv_restart_peer(p, seq);
		p->reset_dec = 1;
	}
	rv_pkt *k = &p->ring[seq & (RV_RING - 1)];
	k->seq = seq;
	k->flags = flags;
	k->len = n - RV_HDR;
	memcpy(k->data, b + RV_HDR, (size_t)k->len);
	k->valid = 1;
	if ((int16_t)(seq - p->max_seq) > 0)
		p->max_seq = seq;
	if (p->prev_arr_us && (int16_t)(seq - p->prev_seq) > 0 && !(flags & RV_F_START)) {
		double D = (now - p->prev_arr_us) / 1000.0 - (uint16_t)(seq - p->prev_seq) * 10.0;
		p->jitter_ms += (fabs(D) - p->jitter_ms) / 16;
	}
	if (!p->prev_arr_us || (int16_t)(seq - p->prev_seq) > 0) {
		p->prev_arr_us = now;
		p->prev_seq = seq;
	}
	p->sender_owd = b[5];
	rv_unlock(&p->mu);
}

static RV_THREAD_RET rv_receiver(void *arg) {
	rv_engine *e = arg;
	uint8_t b[1600];
	rv_thread_priority();
	while (e->running) {
		int64_t now = rv_now_us();
		if (now - e->last_reg_us > (e->registered ? 5000000 : 500000))
			rv_register(e); // until the ack, then every 5 s: the NAT keeps the port open while nobody talks
		if (e->registered && now - e->last_ping_us > 1000000)
			rv_ping(e);
		int n = (int)recv(e->sock, (char *)b, sizeof b, 0);
		if (n <= 0) { // the 20 ms timeout; an error at once is the relay not reachable (ICMP): no spinning on it
			if (rv_now_us() - now < 5000)
				rv_sleep_ms(20);
			continue;
		}
		if (n == 5 && memcmp(b, "RLVA", 4) == 0) {
			e->registered = 1;
			e->state = 2;
		} else if (n == 9 && b[0] == 'p') {
			int64_t t;
			memcpy(&t, b + 1, 8);
			int ms = (int)((rv_now_us() - t) / 1000);
			e->ping_ms = e->ping_ms < 0 ? ms : (e->ping_ms * 3 + ms) / 4;
		} else if (b[0] == 'v')
			rv_insert(e, b, n);
	}
	return 0;
}

// ---- playback side --------------------------------------------------------------------------

// rv_peer_frame: the peer's next 10 ms (played, recovered with the next packet's FEC, or concealed). 0: silent.
static int rv_peer_frame(rv_peerst *p, float *out) {
	uint8_t data[RV_MAXOPUS];
	int len = 0, fec = 0, end = 0;
	rv_lock(&p->mu);
	if (!p->active) {
		rv_unlock(&p->mu);
		return 0;
	}
	if (!p->playing) {
		int buffered = rv_buffered(p);
		if (buffered == 0 || buffered < rv_target(p)) {
			rv_unlock(&p->mu);
			return 0;
		}
		p->playing = 1;
		p->plc_run = 0;
	}
	rv_pkt *k = &p->ring[p->next & (RV_RING - 1)];
	rv_pkt *k2 = &p->ring[(uint16_t)(p->next + 1) & (RV_RING - 1)];
	if (k->valid && k->seq == p->next) {
		len = k->len;
		memcpy(data, k->data, (size_t)len);
		end = k->flags & RV_F_END;
		k->valid = 0;
	} else if (k2->valid && k2->seq == (uint16_t)(p->next + 1)) {
		len = k2->len;
		memcpy(data, k2->data, (size_t)len);
		fec = 1;
	}
	// Nothing for this frame. If later packets came it is lost: conceal it and move on. If nothing newer came the
	// buffer ran dry (a late packet, our sound card a bit fast): conceal without moving on, so the packet is still
	// played when it lands instead of being thrown away as late (the delay grows by a frame; the drift control
	// below takes it back when there is more than needed).
	int stretch = !len && (int16_t)(p->max_seq - (uint16_t)(p->next + 1)) <= 0;
	if (!stretch)
		p->next++;
	// clock drift and jitter that went away: a buffer kept deeper than needed for a while loses one frame
	int buffered = rv_buffered(p), target = rv_target(p);
	p->depth = buffered * 10;
	if (buffered > target + 1) {
		if (++p->over > 20) {
			rv_pkt *x = &p->ring[p->next & (RV_RING - 1)];
			if (x->valid && x->seq == p->next && !(x->flags & RV_F_END)) {
				x->valid = 0;
				p->next++;
			}
			p->over = 0;
		}
	} else
		p->over = 0;
	if (end)
		p->playing = 0, p->active = 0;
	int reset = p->reset_dec;
	p->reset_dec = 0;
	rv_unlock(&p->mu);

	if (reset)
		opus_decoder_ctl(p->dec, OPUS_RESET_STATE);
	int n;
	if (len)
		n = opus_decode_float(p->dec, data, len, out, RV_FRAME, fec);
	else
		n = opus_decode_float(p->dec, NULL, 0, out, RV_FRAME, 0);
	if (n != RV_FRAME)
		memset(out, 0, sizeof(float) * RV_FRAME);
	p->good = p->good * 0.995 + (len && !fec);
	p->bad = p->bad * 0.995 + (!len || fec);
	p->loss_pm = (int)(p->bad * 1000 / (p->good + p->bad + 1e-9));
	if (len && !fec)
		p->plc_run = 0;
	else if (++p->plc_run > RV_PLC_MAX) { // nothing for a while: the sender went quiet without its last packet
		rv_lock(&p->mu);
		p->playing = 0;
		rv_unlock(&p->mu);
	}
	return 1;
}

static void rv_mix_frame(rv_engine *e) {
	float pcm[RV_FRAME];
	memset(e->mix, 0, sizeof e->mix);
	for (int s = 1; s <= RV_SLOTS; s++) {
		rv_peerst *p = &e->peers[s];
		if (s == e->slot || !rv_peer_frame(p, pcm)) {
			p->level = 0;
			p->talking = 0;
			continue;
		}
		double sum = 0;
		for (int i = 0; i < RV_FRAME; i++)
			sum += (double)pcm[i] * pcm[i];
		double db = 10 * log10(sum / RV_FRAME + 1e-12);
		p->level = rv_clampi((int)((db + 60) * 100 / 60), 0, 100);
		p->talking = db > -50;
		if (p->muted || e->deafen)
			continue;
		float g = p->volume / 100.0f * e->volume / 100.0f;
		for (int i = 0; i < RV_FRAME; i++)
			e->mix[i] += pcm[i] * g;
	}
	for (int i = 0; i < RV_FRAME && e->beep_left > 0; i++, e->beep_left--) {
		e->mix[i] += 0.12f * (float)sin(e->beep_ph) * (e->beep_left < 480 ? e->beep_left / 480.0f : 1.0f);
		e->beep_ph += 2 * M_PI * e->beep_hz / RV_RATE;
	}
	for (int i = 0; i < RV_FRAME; i++) { // soft limiter: several voices at once never clip hard
		float x = e->mix[i];
		if (x > 0.8f)
			e->mix[i] = 0.8f + 0.2f * tanhf((x - 0.8f) / 0.2f);
		else if (x < -0.8f)
			e->mix[i] = -0.8f - 0.2f * tanhf((-x - 0.8f) / 0.2f);
	}
}

// rv_on_playback: called by the backend's playback callback, any number of samples (48 kHz mono float).
void rv_on_playback(rv_engine *e, float *out, int n) {
	while (n > 0) {
		if (e->mix_pos == 0 || e->mix_pos == RV_FRAME) {
			rv_mix_frame(e);
			e->mix_pos = 0;
		}
		int k = RV_FRAME - e->mix_pos;
		if (k > n)
			k = n;
		memcpy(out, e->mix + e->mix_pos, sizeof(float) * (size_t)k);
		e->mix_pos += k;
		out += k;
		n -= k;
	}
}

// ---- start / stop ---------------------------------------------------------------------------

static int rv_socket(rv_engine *e, const char *host, int port) {
	char ps[16];
	snprintf(ps, sizeof ps, "%d", port);
	struct addrinfo hints, *res = NULL;
	memset(&hints, 0, sizeof hints);
	hints.ai_family = AF_UNSPEC;
	hints.ai_socktype = SOCK_DGRAM;
	if (getaddrinfo(host, ps, &hints, &res) != 0 || !res)
		return -1;
	e->sock = socket(res->ai_family, SOCK_DGRAM, IPPROTO_UDP);
	int ok = e->sock != RV_BADSOCK && connect(e->sock, res->ai_addr, (int)res->ai_addrlen) == 0;
	freeaddrinfo(res);
	if (!ok)
		return -1;
#ifdef _WIN32
	DWORD tv = 20;
	setsockopt(e->sock, SOL_SOCKET, SO_RCVTIMEO, (const char *)&tv, sizeof tv);
	BOOL no = FALSE; // an ICMP "port unreachable" must not break the socket for good
	DWORD ret;
	WSAIoctl(e->sock, _WSAIOW(IOC_VENDOR, 12), &no, sizeof no, NULL, 0, &ret, NULL, NULL); // SIO_UDP_CONNRESET
#else
	struct timeval tv = {0, 20000};
	setsockopt(e->sock, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
#endif
	int tos = 0xb8; // DSCP EF (expedited forwarding): voice, where routers honour it
	setsockopt(e->sock, IPPROTO_IP, IP_TOS, (const char *)&tos, sizeof tos);
	return 0;
}

static void rv_engine_stop(rv_engine *e) {
	if (!e->running)
		return;
	if (e->audio)
		rv_audio_close(e);
	e->running = 0;
	rv_thread_join(e->sender);
	rv_thread_join(e->receiver);
	rv_closesock(e->sock);
	rv_sem_free(&e->cap_sem);
	opus_encoder_destroy(e->enc);
	for (int i = 1; i <= RV_SLOTS; i++) {
		opus_decoder_destroy(e->peers[i].dec);
		rv_mutex_free(&e->peers[i].mu);
	}
	e->state = 0;
	e->registered = 0;
	e->sent_on = 0;
	e->level = 0;
}

static int rv_engine_start(rv_engine *e, const char *host, int port, const char *token, int slot, int audio) {
	rv_engine_stop(e);
	if (!token || strlen(token) != 32 || slot < 1 || slot > RV_SLOTS)
		return -3;
#ifdef _WIN32
	static int wsa;
	if (!wsa) {
		WSADATA w;
		WSAStartup(MAKEWORD(2, 2), &w);
		wsa = 1;
	}
#endif
	memcpy(e->token, token, 33);
	e->slot = slot;
	e->audio = audio;
	e->cap_w = e->cap_r = 0;
	e->seq = (uint16_t)rv_now_us();
	e->sending = e->hang = e->ptt_tail = 0;
	e->floor_db = -90;
	for (int i = 0; i < RV_FLOOR_WINS; i++)
		e->win_min[i] = -90;
	e->win_i = e->win_n = 0;
	e->registered = 0;
	e->ping_ms = -1;
	e->last_reg_us = e->last_ping_us = 0;
	e->mix_pos = 0;
	e->sock = RV_BADSOCK;
	if (rv_socket(e, host, port) != 0) {
		if (e->sock != RV_BADSOCK)
			rv_closesock(e->sock);
		e->state = -2;
		return -2;
	}
	int err;
	e->enc = opus_encoder_create(RV_RATE, 1, OPUS_APPLICATION_VOIP, &err);
	opus_encoder_ctl(e->enc, OPUS_SET_BITRATE(32000));
	opus_encoder_ctl(e->enc, OPUS_SET_SIGNAL(OPUS_SIGNAL_VOICE));
	opus_encoder_ctl(e->enc, OPUS_SET_INBAND_FEC(1)); // each packet carries the previous one at low rate
	opus_encoder_ctl(e->enc, OPUS_SET_PACKET_LOSS_PERC(10));
	opus_encoder_ctl(e->enc, OPUS_SET_COMPLEXITY(7));
	for (int i = 1; i <= RV_SLOTS; i++) {
		rv_peerst *p = &e->peers[i];
		int vol = p->volume, muted = p->muted;
		memset(p, 0, sizeof *p);
		p->volume = vol;
		p->muted = muted;
		rv_mutex_init(&p->mu);
		p->dec = opus_decoder_create(RV_RATE, 1, &err);
	}
	rv_sem_init(&e->cap_sem);
	e->running = 1;
	e->state = 1;
	rv_thread_start(&e->sender, rv_sender, e);
	rv_thread_start(&e->receiver, rv_receiver, e);
	if (audio && rv_audio_open(e, e->echo) != 0) {
		rv_engine_stop(e);
		e->state = -1;
		return -1;
	}
	return 0;
}

static void rv_api_lock(void) {
	if (!g_api_init) { // the first call comes from one thread (the launcher starts the engine once it is online)
		rv_mutex_init(&g_api);
		rv_engine_defaults(&g_rv);
		g_api_init = 1;
	}
	rv_lock(&g_api);
}

RV_API int rv_start(const char *host, int port, const char *token, int slot) {
	rv_api_lock();
	int r = rv_engine_start(&g_rv, host, port, token, slot, 1);
	rv_unlock(&g_api);
	return r;
}

RV_API void rv_stop(void) {
	rv_api_lock();
	rv_engine_stop(&g_rv);
	rv_unlock(&g_api);
}

static int rv_engine_set(rv_engine *e, int key, int v) {
	switch (key) {
	case RV_MUTED:
		if (e->muted != !!v) {
			e->muted = !!v;
			if (e->running && e->audio && e->echo) { // echo cancellation is one unit with the microphone: reopened
				rv_audio_close(e);
				if (rv_audio_open(e, 1) != 0)
					e->state = -1;
			} else if (e->running && e->audio)
				rv_audio_capture(e, !v); // muted: the microphone is closed, not just ignored
		}
		break;
	case RV_PTT: e->ptt = !!v; break;
	case RV_PTT_DOWN: e->ptt_down = !!v; break;
	case RV_SENSE: e->sense = rv_clampi(v, 0, 100); break;
	case RV_MIC_GAIN: e->mic_gain = rv_clampi(v, 0, 400); break;
	case RV_VOLUME: e->volume = rv_clampi(v, 0, 200); break;
	case RV_ECHO: e->echo = !!v; break;
	case RV_DEAFEN: e->deafen = !!v; break;
	case RV_BEEP: // 1: up (microphone open), 2: down (closed)
		e->beep_hz = v == 1 ? 880 : 440;
		e->beep_left = 4800;
		break;
	default: return -1;
	}
	return 0;
}

RV_API int rv_set(int key, int value) {
	rv_api_lock();
	int r = rv_engine_set(&g_rv, key, value);
	rv_unlock(&g_api);
	return r;
}

RV_API int rv_peer(int slot, int volume, int muted) {
	if (slot < 1 || slot > RV_SLOTS)
		return -1;
	rv_api_lock();
	g_rv.peers[slot].volume = rv_clampi(volume, 0, 200);
	g_rv.peers[slot].muted = !!muted;
	rv_unlock(&g_api);
	return 0;
}

RV_API int rv_device(int capture, const char *name) {
	rv_api_lock();
	char *d = capture ? g_rv.dev_cap : g_rv.dev_play;
	snprintf(d, sizeof g_rv.dev_cap, "%s", name ? name : "");
	int r = 0;
	if (g_rv.running && g_rv.audio) { // reopened at once with the new device
		rv_audio_close(&g_rv);
		r = rv_audio_open(&g_rv, g_rv.echo);
		if (r != 0)
			g_rv.state = -1;
	}
	rv_unlock(&g_api);
	return r;
}

RV_API int rv_devices(int capture, char *buf, int len) { return rv_audio_devices(capture, buf, len); }

static int rv_engine_stats(rv_engine *e, int *out, int n) {
	int st[RV_ST_N];
	st[RV_ST_STATE] = e->state;
	st[RV_ST_PING] = e->ping_ms;
	st[RV_ST_LEVEL] = e->muted ? 0 : e->level;
	st[RV_ST_SENDING] = e->sent_on;
	st[RV_ST_SLOT] = e->slot;
	for (int s = 1; s <= RV_SLOTS; s++) {
		rv_peerst *p = &e->peers[s];
		int *q = st + RV_ST_PEERS + (s - 1) * RV_PEER_N;
		q[0] = p->level;
		q[1] = p->talking;
		// mouth to ear: their capture period, frame and Opus lookahead (~22 ms), their way to the relay and ours, our
		// jitter buffer and our playback period (~10 ms)
		q[2] = p->active && e->ping_ms >= 0 ? 22 + p->sender_owd + e->ping_ms / 2 + p->depth + 10 : -1;
		q[3] = p->loss_pm;
		q[4] = p->depth;
	}
	if (n > RV_ST_N)
		n = RV_ST_N;
	memcpy(out, st, sizeof(int) * (size_t)n);
	return n;
}

RV_API int rv_stats(int *out, int n) { return rv_engine_stats(&g_rv, out, n); }
