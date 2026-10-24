// devices: the engine on this machine's real speakers. A second engine with no devices talks (a tone) through the
// test relay; the first plays it at 1% volume with its microphone muted (never opened: no permission asked). Checks
// that the playback callback pulls and decodes, and prints the device's real period.
//
//   cc -O2 -I../include -I.. devices.c ../lib/darwin-arm64/libopus.a -framework CoreAudio -framework AudioToolbox \
//      -framework CoreFoundation -o devices && ./devices
#include "../engine.c"
#include "../audio_ma.c"
#include "../audio_vpio.c"
#include "relay.h"

int main(void) {
	relay_start();
	rv_engine *spk = calloc(1, sizeof(rv_engine)), *talker = calloc(1, sizeof(rv_engine));
	rv_engine_defaults(spk);
	rv_engine_defaults(talker);
	spk->muted = 1;
	spk->volume = 1;
	int r = rv_engine_start(spk, "127.0.0.1", R.port, tok(1), 1, 1);
	printf("start with the speakers: %d (state %d)\n", r, spk->state);
	if (r != 0)
		return 1;
	rv_ma *m = spk->backend;
	printf("playback device: %s, %u Hz, period %u frames (%.1f ms) x %u, capture opened: %d\n", m->play.playback.name,
	       m->play.playback.internalSampleRate, m->play.playback.internalPeriodSizeInFrames,
	       m->play.playback.internalPeriodSizeInFrames * 1000.0 / m->play.playback.internalSampleRate,
	       m->play.playback.internalPeriods, m->cap_on);
	rv_engine_start(talker, "127.0.0.1", R.port, tok(2), 2, 0);
	float pcm[RV_FRAME];
	double ph = 0;
	int maxlevel = 0, frames = 0;
	int64_t t0 = rv_now_us();
	for (int k = 0; k < 300; k++) { // 3 s: 1 s silent, 2 s of tone
		int64_t due = t0 + k * 10000, now = rv_now_us();
		if (due > now)
			usleep((useconds_t)(due - now));
		for (int i = 0; i < RV_FRAME; i++, ph += 2 * M_PI * 440 / RV_RATE)
			pcm[i] = k >= 100 ? 0.25f * (float)sin(ph) : 0;
		rv_on_capture(talker, pcm, RV_FRAME);
		int l = spk->peers[2].level;
		if (l > maxlevel)
			maxlevel = l;
		frames += spk->peers[2].talking;
		if (k == 250) {
			int q[RV_ST_N];
			rv_engine_stats(spk, q, RV_ST_N);
			printf("mid-stream: active %d playing %d ping %d owd %d depth %d estimate %d\n", spk->peers[2].active,
			       spk->peers[2].playing, q[RV_ST_PING], spk->peers[2].sender_owd, q[RV_ST_PEERS + 4 + RV_PEER_N],
			       q[RV_ST_PEERS + 2 + RV_PEER_N]);
		}
	}
	int st[RV_ST_N];
	rv_engine_stats(spk, st, RV_ST_N);
	printf("heard through the speakers: level %d, talking in %d of 200 polls, ping %d ms, buffer %d ms, estimate %d ms\n",
	       maxlevel, frames, st[RV_ST_PING], st[RV_ST_PEERS + 4 + RV_PEER_N], st[RV_ST_PEERS + 2 + RV_PEER_N]);
	rv_engine_stop(talker);
	rv_engine_stop(spk);
	int ok = maxlevel > 50 && frames > 100;
	puts(ok ? "ok" : "FAIL");
	return !ok;
}
