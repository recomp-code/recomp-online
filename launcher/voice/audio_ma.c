// The devices through miniaudio (CoreAudio on macOS, WASAPI on Windows): capture and playback each on their own
// device at 10 ms periods, 48 kHz mono float (miniaudio converts from whatever the device runs at). With RV_ECHO on
// macOS the devices are Apple's voice processing unit instead (audio_vpio.c).
#define MINIAUDIO_IMPLEMENTATION
#define MA_NO_DECODING
#define MA_NO_ENCODING
#define MA_NO_GENERATION
#define MA_NO_RESOURCE_MANAGER
#define MA_NO_NODE_GRAPH
#define MA_NO_ENGINE
#define MA_ENABLE_ONLY_SPECIFIC_BACKENDS
#define MA_ENABLE_COREAUDIO
#define MA_ENABLE_WASAPI
#include "miniaudio.h"

#include <string.h>

typedef struct rv_engine rv_engine;
void rv_on_capture(rv_engine *e, const float *in, int n);
void rv_on_playback(rv_engine *e, float *out, int n);
const char *rv_engine_device(rv_engine *e, int capture);
void *rv_engine_backend(rv_engine *e);
void rv_engine_set_backend(rv_engine *e, void *b);
int rv_engine_muted(rv_engine *e);

#ifdef __APPLE__
int rv_vpio_open(rv_engine *e);
void rv_vpio_close(rv_engine *e);
int rv_vpio_active(rv_engine *e);
#endif

typedef struct {
	int kind; // 1 (audio_vpio.c's is 2)
	ma_device cap, play;
	int cap_on, play_on;
} rv_ma;

static ma_context g_ctx;
static int g_ctx_ok;

static ma_context *rv_ctx(void) {
	if (!g_ctx_ok && ma_context_init(NULL, 0, NULL, &g_ctx) == MA_SUCCESS)
		g_ctx_ok = 1;
	return g_ctx_ok ? &g_ctx : NULL;
}

static void rv_cap_cb(ma_device *d, void *out, const void *in, ma_uint32 n) {
	(void)out;
	rv_on_capture((rv_engine *)d->pUserData, (const float *)in, (int)n);
}

static void rv_play_cb(ma_device *d, void *out, const void *in, ma_uint32 n) {
	(void)in;
	rv_on_playback((rv_engine *)d->pUserData, (float *)out, (int)n);
}

// rv_find: the id of the device with that name; 0 (the default device) when it is gone or the name is "".
static int rv_find(int capture, const char *name, ma_device_id *id) {
	ma_context *c = rv_ctx();
	ma_device_info *play, *cap;
	ma_uint32 np, nc;
	if (!c || !name[0] || ma_context_get_devices(c, &play, &np, &cap, &nc) != MA_SUCCESS)
		return 0;
	ma_device_info *l = capture ? cap : play;
	ma_uint32 n = capture ? nc : np;
	for (ma_uint32 i = 0; i < n; i++)
		if (strcmp(l[i].name, name) == 0) {
			*id = l[i].id;
			return 1;
		}
	return 0;
}

static int rv_ma_device(rv_engine *e, ma_device *d, int capture) {
	ma_device_config c = ma_device_config_init(capture ? ma_device_type_capture : ma_device_type_playback);
	ma_device_id id;
	int has = rv_find(capture, rv_engine_device(e, capture), &id);
	if (capture) {
		c.capture.format = ma_format_f32;
		c.capture.channels = 1;
		c.capture.pDeviceID = has ? &id : NULL;
		c.capture.shareMode = ma_share_mode_shared;
		c.dataCallback = rv_cap_cb;
	} else {
		c.playback.format = ma_format_f32;
		c.playback.channels = 1;
		c.playback.pDeviceID = has ? &id : NULL;
		c.dataCallback = rv_play_cb;
	}
	c.sampleRate = 48000;
	c.periodSizeInMilliseconds = 10;
	c.periods = 2;
	c.performanceProfile = ma_performance_profile_low_latency;
	c.noPreSilencedOutputBuffer = MA_TRUE;
	c.noClip = MA_TRUE; // rv_mix_frame limits the mix itself
	c.pUserData = e;
	c.wasapi.noAutoConvertSRC = MA_TRUE; // miniaudio's resampler, not the slower one of WASAPI's shared mode
	c.coreaudio.allowNominalSampleRateChange = MA_FALSE;
	if (ma_device_init(rv_ctx(), &c, d) != MA_SUCCESS)
		return -1;
	if (ma_device_start(d) != MA_SUCCESS) {
		ma_device_uninit(d);
		return -1;
	}
	return 0;
}

int rv_audio_open(rv_engine *e, int echo) {
#ifdef __APPLE__
	if (echo && !rv_engine_muted(e) && rv_vpio_open(e) == 0) // one unit, with the microphone: not while muted
		return 0;
#else
	(void)echo;
#endif
	if (!rv_ctx())
		return -1;
	rv_ma *m = calloc(1, sizeof *m);
	m->kind = 1;
	if (rv_ma_device(e, &m->play, 0) != 0) {
		free(m);
		return -1;
	}
	m->play_on = 1;
	// muted, the microphone is not even opened; none plugged (or not allowed) still hears the others
	m->cap_on = !rv_engine_muted(e) && rv_ma_device(e, &m->cap, 1) == 0;
	rv_engine_set_backend(e, m);
	return 0;
}

void rv_audio_close(rv_engine *e) {
#ifdef __APPLE__
	if (rv_vpio_active(e)) {
		rv_vpio_close(e);
		return;
	}
#endif
	rv_ma *m = rv_engine_backend(e);
	if (!m)
		return;
	if (m->cap_on)
		ma_device_uninit(&m->cap);
	if (m->play_on)
		ma_device_uninit(&m->play);
	free(m);
	rv_engine_set_backend(e, NULL);
}

// rv_audio_capture: the microphone opened or closed (muted: the system shows it is not in use).
void rv_audio_capture(rv_engine *e, int on) {
#ifdef __APPLE__
	if (rv_vpio_active(e))
		return; // one unit for both directions: muted, its frames are dropped (rv_frame_out)
#endif
	rv_ma *m = rv_engine_backend(e);
	if (!m)
		return;
	if (on && !m->cap_on)
		m->cap_on = rv_ma_device(e, &m->cap, 1) == 0;
	else if (!on && m->cap_on) {
		ma_device_uninit(&m->cap);
		m->cap_on = 0;
	}
}

int rv_audio_devices(int capture, char *buf, int len) {
	ma_context *c = rv_ctx();
	ma_device_info *play, *cap;
	ma_uint32 np, nc;
	if (!c || ma_context_get_devices(c, &play, &np, &cap, &nc) != MA_SUCCESS)
		return 0;
	ma_device_info *l = capture ? cap : play;
	ma_uint32 n = capture ? nc : np;
	int need = 0;
	for (ma_uint32 i = 0; i < n; i++) {
		int k = (int)strlen(l[i].name);
		if (buf && need + k + 1 <= len) {
			memcpy(buf + need, l[i].name, (size_t)k);
			buf[need + k] = i + 1 < n ? '\n' : 0;
		}
		need += k + 1;
	}
	if (buf && len > 0 && need > len)
		buf[len - 1] = 0;
	return need;
}
