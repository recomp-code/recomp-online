// e2e: four voice engines with no audio devices, talking through a relay of the same protocol as the server's
// (server/voice.go) that can lose datagrams and delay them at random. Every 10 ms (real time) each engine is fed one
// captured frame and asked for one played frame, as its devices would. Tones in, tones detected out.
//
//   cc -O2 -I../include e2e.c ../lib/darwin-arm64/libopus.a -o e2e && ./e2e
//
// What it measures is the engine (gate, Opus, jitter buffer, FEC, concealment, mix); the devices add their own
// buffers on top (about 10 ms each way at the periods audio_ma.c asks for).
#include "../engine.c"

#include <assert.h>

// no devices
int rv_audio_open(rv_engine *e, int echo) { (void)e, (void)echo; return -1; }
void rv_audio_close(rv_engine *e) { (void)e; }
void rv_audio_capture(rv_engine *e, int on) { (void)e, (void)on; }
int rv_audio_devices(int capture, char *buf, int len) { (void)capture, (void)buf, (void)len; return 0; }

#include "relay.h"

// ---- the harness ----------------------------------------------------------------------------

#define N 4
static rv_engine *E[N + 1];
static float freq[N + 1] = {0, 500, 900, 1300, 1700};

static double goertzel(const float *x, int n, double f) {
	double w = 2 * M_PI * f / RV_RATE, c = 2 * cos(w), s1 = 0, s2 = 0;
	for (int i = 0; i < n; i++) {
		double s = x[i] + c * s1 - s2;
		s2 = s1;
		s1 = s;
	}
	return sqrt(s1 * s1 + s2 * s2 - c * s1 * s2) / (n / 2.0); // amplitude of that tone
}

typedef struct {
	int talk[N + 1];    // engine i talks (its tone) this tick
	float noise[N + 1]; // or feeds noise of that amplitude
	int skip_pull[N + 1], extra_pull[N + 1];
} tick_in;

typedef struct {
	double amp[N + 1][N + 1]; // [listener][talker]: that talker's tone heard by that listener
	int onset_sample[N + 1][N + 1];
} tick_out;

static int64_t t0;
static long ticks;
static double phase[N + 1];

static void tick(const tick_in *in, tick_out *out) {
	int64_t due = t0 + ticks * 10000;
	int64_t now = rv_now_us();
	if (due > now)
		usleep((useconds_t)(due - now));
	ticks++;
	float pcm[RV_FRAME], play[RV_FRAME];
	for (int i = 1; i <= N; i++) {
		for (int k = 0; k < RV_FRAME; k++) {
			float v = 0;
			if (in->talk[i])
				v = 0.25f * (float)sin(phase[i]);
			else if (in->noise[i] > 0)
				v = in->noise[i] * ((float)rand() / (float)RAND_MAX * 2 - 1);
			phase[i] += 2 * M_PI * freq[i] / RV_RATE;
			pcm[k] = v;
		}
		rv_on_capture(E[i], pcm, RV_FRAME);
	}
	for (int i = 1; i <= N; i++) {
		int pulls = in->skip_pull[i] ? 0 : 1 + in->extra_pull[i];
		memset(out->amp[i], 0, sizeof out->amp[i]);
		for (int p = 0; p < pulls; p++) {
			rv_on_playback(E[i], play, RV_FRAME);
			for (int t = 1; t <= N; t++) {
				double a = goertzel(play, RV_FRAME, freq[t]);
				if (a > out->amp[i][t])
					out->amp[i][t] = a;
				out->onset_sample[i][t] = RV_FRAME;
				for (int k = 0; k < RV_FRAME; k++)
					if (fabsf(play[k]) > 0.05f) {
						out->onset_sample[i][t] = k;
						break;
					}
			}
		}
	}
}

static void start_all(void) {
	for (int i = 1; i <= N; i++) {
		if (!E[i]) {
			E[i] = calloc(1, sizeof(rv_engine));
			rv_engine_defaults(E[i]);
		}
		assert(rv_engine_start(E[i], "127.0.0.1", R.port, tok(i), i, 0) == 0);
	}
	t0 = rv_now_us();
	ticks = 0;
	tick_in in = {0};
	tick_out out;
	for (int k = 0; k < 100; k++) // a second to register and ping
		tick(&in, &out);
	for (int i = 1; i <= N; i++)
		assert(E[i]->registered);
}

static void stop_all(void) {
	for (int i = 1; i <= N; i++)
		rv_engine_stop(E[i]);
}

static int cmpd(const void *a, const void *b) { return *(const double *)a < *(const double *)b ? -1 : 1; }

// bursts: talker 1 talks 400 ms every 800 ms; listener 2 measures the latency of each onset and the frames of the
// burst it missed (a gap: less than a third of the tone, the first and last 3 frames of the burst aside).
static void bursts(const char *name, int loss, int jmin, int jmax, int n) {
	R.loss_pct = loss, R.jit_min_ms = jmin, R.jit_max_ms = jmax;
	start_all();
	double lat[64];
	int nl = 0, gaps = 0, frames = 0;
	for (int b = 0; b < n; b++) {
		tick_in in = {0};
		tick_out out;
		long onset = -1;
		for (int k = 0; k < 80; k++) {
			in.talk[1] = k < 40;
			tick(&in, &out);
			int heard = out.amp[2][1] > 0.25 / 3;
			if (heard && onset < 0 && k < 60) {
				onset = k;
				lat[nl++] = k * 10.0 + out.onset_sample[2][1] / 48.0 - 10; // the frame fed at k=0 is captured 10 ms in
			}
			if (onset >= 0 && k >= onset + 3 && k < 37 + (onset > 0 ? onset : 0)) {
				frames++;
				gaps += !heard;
			}
		}
	}
	stop_all();
	qsort(lat, (size_t)nl, sizeof lat[0], cmpd);
	printf("%-34s onsets %2d/%d  latency p50 %5.1f ms  max %5.1f ms  gaps %d/%d frames\n", name, nl, n,
	       nl ? lat[nl / 2] : -1, nl ? lat[nl - 1] : -1, gaps, frames);
}

int main(void) {
	srand(7);
	relay_start();
	int fails = 0;

	puts("== latency and continuity, talker -> listener (engine only, no devices)");
	bursts("clean network", 0, 0, 0, 12);
	bursts("5% loss", 5, 0, 0, 12);
	bursts("15% loss", 15, 0, 0, 12);
	bursts("jitter 0-30 ms", 0, 0, 30, 12);
	bursts("jitter 0-30 ms + 5% loss", 5, 0, 30, 12);

	puts("== four at once, local mute, self mute, the gate");
	R.loss_pct = R.jit_min_ms = R.jit_max_ms = 0;
	start_all();
	tick_in in = {0};
	tick_out out;
	in.talk[1] = in.talk[3] = 1;
	double a21 = 0, a23 = 0, a41 = 0, a43 = 0;
	for (int k = 0; k < 50; k++) {
		tick(&in, &out);
		if (k >= 10) {
			a21 += out.amp[2][1], a23 += out.amp[2][3], a41 += out.amp[4][1], a43 += out.amp[4][3];
		}
	}
	printf("1 and 3 talk together: 2 hears %.2f / %.2f, 4 hears %.2f / %.2f (each sent at 0.25)\n", a21 / 40, a23 / 40,
	       a41 / 40, a43 / 40);
	fails += a21 / 40 < 0.15 || a23 / 40 < 0.15 || a41 / 40 < 0.15 || a43 / 40 < 0.15;

	E[2]->peers[1].muted = 1; // 2 mutes 1 for themselves
	a21 = a23 = a41 = 0;
	for (int k = 0; k < 30; k++) {
		tick(&in, &out);
		if (k >= 5)
			a21 += out.amp[2][1], a23 += out.amp[2][3], a41 += out.amp[4][1];
	}
	printf("2 mutes 1 locally: 2 hears 1 %.3f, 3 %.2f; 4 still hears 1 %.2f\n", a21 / 25, a23 / 25, a41 / 25);
	fails += a21 / 25 > 0.01 || a23 / 25 < 0.15 || a41 / 25 < 0.15;
	E[2]->peers[1].muted = 0;

	rv_engine_set(E[1], RV_MUTED, 1);
	for (int k = 0; k < 10; k++)
		tick(&in, &out);
	int before = R.from[1];
	for (int k = 0; k < 50; k++)
		tick(&in, &out);
	printf("1 mutes itself: %d datagrams sent in 500 ms of talking\n", R.from[1] - before);
	fails += R.from[1] != before;
	rv_engine_set(E[1], RV_MUTED, 0);

	memset(&in, 0, sizeof in);
	in.noise[1] = 0.0003f; // -70 dBFS of hiss
	for (int k = 0; k < 40; k++)
		tick(&in, &out);
	before = R.from[1];
	for (int k = 0; k < 100; k++)
		tick(&in, &out);
	printf("the gate with -70 dBFS hiss: %d datagrams in 1 s\n", R.from[1] - before);
	fails += R.from[1] != before;
	in.noise[1] = 0.03f; // -30 dBFS: a fan close to the microphone, steady: the floor follows it
	for (int k = 0; k < 600; k++)
		tick(&in, &out);
	before = R.from[1];
	for (int k = 0; k < 100; k++)
		tick(&in, &out);
	printf("the gate after 6 s of a steady -30 dBFS fan: %d datagrams in 1 s\n", R.from[1] - before);
	fails += R.from[1] - before > 5;
	in.talk[1] = 1; // and a voice over the fan opens it again
	before = R.from[1];
	for (int k = 0; k < 50; k++)
		tick(&in, &out);
	printf("a voice over that fan: %d datagrams in 500 ms\n", R.from[1] - before);
	fails += R.from[1] - before < 45;
	in.talk[1] = 0;
	in.noise[1] = 0;
	stop_all();

	puts("== clock drift: the listener's sound card 1% slow, then 1% fast (60 s of talking each, push to talk held)");
	for (int dir = 0; dir < 2; dir++) {
		start_all();
		rv_engine_set(E[1], RV_PTT, 1);
		rv_engine_set(E[1], RV_PTT_DOWN, 1);
		memset(&in, 0, sizeof in);
		in.talk[1] = 1;
		int maxdepth = 0, gaps = 0, frames = 0;
		for (int k = 0; k < 6000; k++) {
			in.skip_pull[2] = dir == 0 && k % 100 == 50;
			in.extra_pull[2] = dir == 1 && k % 100 == 50;
			tick(&in, &out);
			if (k > 50 && !in.skip_pull[2]) { // a tick the slow card skips plays nothing by design
				frames++;
				gaps += out.amp[2][1] < 0.25 / 3;
				if (E[2]->peers[1].depth > maxdepth)
					maxdepth = E[2]->peers[1].depth;
			}
		}
		printf("%s: deepest buffer %d ms, final %d ms, gaps %d/%d frames\n", dir == 0 ? "slow" : "fast", maxdepth,
		       E[2]->peers[1].depth, gaps, frames);
		fails += maxdepth > 80 || gaps > frames / 100;
		rv_engine_set(E[1], RV_PTT, 0);
		stop_all();
	}
	printf(fails ? "FAIL (%d)\n" : "ok\n", fails);
	return fails != 0;
}
