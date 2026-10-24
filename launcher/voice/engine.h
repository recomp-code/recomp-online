// recompvoice: the launcher's voice chat engine (C, so the audio path never waits on Go's garbage collector).
//
// 48 kHz mono, 10 ms frames, Opus (VOIP, in-band FEC) over the relay's UDP port (server/voice.go has the protocol).
// Three kinds of threads: the audio callbacks (capture only copies into a ring and wakes the sender; playback
// decodes and mixes the others), the sender (gain, gate, Opus, send) and the receiver (jitter buffers, register
// and ping timers). Only ints cross this interface, so Windows can call it from Go without cgo (voice_windows.go).
#ifndef RECOMPVOICE_H
#define RECOMPVOICE_H

#ifdef _WIN32
#define RV_API __declspec(dllexport)
#else
#define RV_API
#endif

#define RV_SLOTS 4

// rv_set keys
enum {
	RV_MUTED = 1,    // 1: the microphone is closed (nothing is captured or sent)
	RV_PTT = 2,      // 1: push to talk (sends only while RV_PTT_DOWN), 0: voice activity
	RV_PTT_DOWN = 3, // the push-to-talk key is held
	RV_SENSE = 4,    // 0-100: how easily the gate opens (50 default)
	RV_MIC_GAIN = 5, // percent, 0-400 (100 default)
	RV_VOLUME = 6,   // output, percent, 0-200 (100 default)
	RV_ECHO = 7,     // 1: echo cancellation (macOS: Apple's voice processing; takes effect on the next start)
	RV_DEAFEN = 8,   // 1: nothing is played
	RV_BEEP = 9,     // a short tone in the output: 1 high (microphone open), 2 low (closed)
};

// rv_stats indexes (ints)
enum {
	RV_ST_STATE = 0,    // 0 stopped, 1 registering, 2 live, -1 no audio device, -2 network error
	RV_ST_PING = 1,     // round trip to the relay, ms (-1 unknown)
	RV_ST_LEVEL = 2,    // own microphone level 0-100
	RV_ST_SENDING = 3,  // 1 while our voice goes out
	RV_ST_SLOT = 4,     // our place
	RV_ST_PEERS = 5,    // then RV_SLOTS blocks of RV_PEER_N ints, slot 1 first
	RV_PEER_N = 5,      // per peer: level 0-100, talking 0/1, mouth-to-ear estimate ms (-1 unknown), loss per mille,
	                    // jitter buffer ms
	RV_ST_N = RV_ST_PEERS + RV_SLOTS * RV_PEER_N,
};

#ifdef __cplusplus
extern "C" {
#endif

// rv_start opens the audio devices and registers with the relay (host "a.b.c.d" or a name, port, the player's voice
// token, their slot). 0 on success, <0 on error (the state says which). Starting again restarts.
RV_API int rv_start(const char *host, int port, const char *token, int slot);
RV_API void rv_stop(void);
RV_API int rv_set(int key, int value);
// rv_peer: the local volume (percent, 0-200) and mute of the player in that slot.
RV_API int rv_peer(int slot, int volume, int muted);
// rv_device: the capture (capture=1) or playback device by name, "" for the system's default. Applies at once.
RV_API int rv_device(int capture, const char *name);
// rv_devices writes the device names ('\n' between them) into buf; returns the length needed.
RV_API int rv_devices(int capture, char *buf, int len);
// rv_stats fills out (n ints, RV_ST_N at most); returns how many were written.
RV_API int rv_stats(int *out, int n);

#ifdef __cplusplus
}
#endif

#endif
