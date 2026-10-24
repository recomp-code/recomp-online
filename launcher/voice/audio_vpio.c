// macOS echo cancellation (RV_ECHO): Apple's voice processing I/O unit, one unit for the microphone and the
// speakers. It cancels what it plays itself (the others' voices) from the microphone, and suppresses noise. Other
// audio (the game) is ducked as little as macOS 14 allows. It always uses the system's default devices.
#ifdef __APPLE__
#include <AudioToolbox/AudioToolbox.h>
#include <stdlib.h>

typedef struct rv_engine rv_engine;
void rv_on_capture(rv_engine *e, const float *in, int n);
void rv_on_playback(rv_engine *e, float *out, int n);
void *rv_engine_backend(rv_engine *e);
void rv_engine_set_backend(rv_engine *e, void *b);

#define RV_VPIO_MAX 4096

typedef struct {
	int kind; // 2 (audio_ma.c's backend is 1)
	AudioUnit unit;
	rv_engine *e;
	float buf[RV_VPIO_MAX];
} rv_vpio;

static OSStatus rv_vpio_in(void *ref, AudioUnitRenderActionFlags *flags, const AudioTimeStamp *ts, UInt32 bus, UInt32 n,
                           AudioBufferList *io) {
	(void)bus;
	(void)io;
	rv_vpio *v = ref;
	if (n > RV_VPIO_MAX)
		return noErr;
	AudioBufferList bl;
	bl.mNumberBuffers = 1;
	bl.mBuffers[0].mNumberChannels = 1;
	bl.mBuffers[0].mDataByteSize = n * sizeof(float);
	bl.mBuffers[0].mData = v->buf;
	if (AudioUnitRender(v->unit, flags, ts, 1, n, &bl) == noErr)
		rv_on_capture(v->e, v->buf, (int)n);
	return noErr;
}

static OSStatus rv_vpio_out(void *ref, AudioUnitRenderActionFlags *flags, const AudioTimeStamp *ts, UInt32 bus, UInt32 n,
                            AudioBufferList *io) {
	(void)flags;
	(void)ts;
	(void)bus;
	rv_vpio *v = ref;
	rv_on_playback(v->e, (float *)io->mBuffers[0].mData, (int)n);
	return noErr;
}

int rv_vpio_active(rv_engine *e) {
	int *b = rv_engine_backend(e);
	return b && *b == 2;
}

int rv_vpio_open(rv_engine *e) {
	AudioComponentDescription d = {kAudioUnitType_Output, kAudioUnitSubType_VoiceProcessingIO, kAudioUnitManufacturer_Apple, 0, 0};
	AudioComponent c = AudioComponentFindNext(NULL, &d);
	rv_vpio *v = calloc(1, sizeof *v);
	v->kind = 2;
	v->e = e;
	if (!c || AudioComponentInstanceNew(c, &v->unit) != noErr) {
		free(v);
		return -1;
	}
	UInt32 one = 1, max = RV_VPIO_MAX;
	AudioStreamBasicDescription f = {0};
	f.mSampleRate = 48000;
	f.mFormatID = kAudioFormatLinearPCM;
	f.mFormatFlags = kAudioFormatFlagIsFloat | kAudioFormatFlagIsPacked | kAudioFormatFlagIsNonInterleaved;
	f.mBytesPerPacket = f.mBytesPerFrame = 4;
	f.mFramesPerPacket = 1;
	f.mChannelsPerFrame = 1;
	f.mBitsPerChannel = 32;
	AURenderCallbackStruct in = {rv_vpio_in, v}, out = {rv_vpio_out, v};
	AUVoiceIOOtherAudioDuckingConfiguration duck = {true, kAUVoiceIOOtherAudioDuckingLevelMin};
	OSStatus s = AudioUnitSetProperty(v->unit, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Input, 1, &one, sizeof one);
	s |= AudioUnitSetProperty(v->unit, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Output, 1, &f, sizeof f);
	s |= AudioUnitSetProperty(v->unit, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Input, 0, &f, sizeof f);
	s |= AudioUnitSetProperty(v->unit, kAudioUnitProperty_MaximumFramesPerSlice, kAudioUnitScope_Global, 0, &max, sizeof max);
	s |= AudioUnitSetProperty(v->unit, kAudioOutputUnitProperty_SetInputCallback, kAudioUnitScope_Global, 1, &in, sizeof in);
	s |= AudioUnitSetProperty(v->unit, kAudioUnitProperty_SetRenderCallback, kAudioUnitScope_Input, 0, &out, sizeof out);
	AudioUnitSetProperty(v->unit, kAUVoiceIOProperty_OtherAudioDuckingConfiguration, kAudioUnitScope_Global, 0, &duck, sizeof duck);
	if (s != noErr || AudioUnitInitialize(v->unit) != noErr) {
		AudioComponentInstanceDispose(v->unit);
		free(v);
		return -1;
	}
	rv_engine_set_backend(e, v);
	if (AudioOutputUnitStart(v->unit) != noErr) {
		rv_engine_set_backend(e, NULL);
		AudioUnitUninitialize(v->unit);
		AudioComponentInstanceDispose(v->unit);
		free(v);
		return -1;
	}
	return 0;
}

void rv_vpio_close(rv_engine *e) {
	rv_vpio *v = rv_engine_backend(e);
	AudioOutputUnitStop(v->unit);
	AudioUnitUninitialize(v->unit);
	AudioComponentInstanceDispose(v->unit);
	rv_engine_set_backend(e, NULL);
	free(v);
}
#endif
