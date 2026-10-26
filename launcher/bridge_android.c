// [android] The JNI side of the launcher's bridge (main_android.go): the app's RecompBridge (Java) calls nativeInit /
// nativeCall; Go calls back through rbEmit (an event for the WebView) and rbPlatform (an Android service: the file
// picker, starting the game, opening a link...). Every string crossing here is ASCII JSON (non-ASCII is \u-escaped on
// both sides), so JNI's modified UTF-8 never matters.
#include <jni.h>
#include <stdlib.h>
#include <string.h>
#include "_cgo_export.h"

static JavaVM *g_vm;
static jclass g_cls;
static jmethodID g_onEvent, g_platform;

JNIEXPORT jint JNI_OnLoad(JavaVM *vm, void *reserved)
{
    (void)reserved;
    g_vm = vm;
    return JNI_VERSION_1_6;
}

static JNIEnv *attach(int *detach)
{
    JNIEnv *e = NULL;
    *detach = 0;
    if ((*g_vm)->GetEnv(g_vm, (void **)&e, JNI_VERSION_1_6) == JNI_EDETACHED)
    {
        (*g_vm)->AttachCurrentThread(g_vm, &e, NULL);
        *detach = 1;
    }
    return e;
}

void rbEmit(const char *name, const char *json)
{
    int detach;
    JNIEnv *e = attach(&detach);
    if (!e || !g_cls) return;
    jstring n = (*e)->NewStringUTF(e, name), j = (*e)->NewStringUTF(e, json);
    (*e)->CallStaticVoidMethod(e, g_cls, g_onEvent, n, j);
    if ((*e)->ExceptionCheck(e)) (*e)->ExceptionClear(e);
    (*e)->DeleteLocalRef(e, n);
    (*e)->DeleteLocalRef(e, j);
    if (detach) (*g_vm)->DetachCurrentThread(g_vm);
}

// The answer is malloc'd (the caller frees it); NULL when Java threw.
char *rbPlatform(const char *op, const char *json)
{
    int detach;
    JNIEnv *e = attach(&detach);
    if (!e || !g_cls) return NULL;
    jstring o = (*e)->NewStringUTF(e, op), j = (*e)->NewStringUTF(e, json);
    jstring r = (jstring)(*e)->CallStaticObjectMethod(e, g_cls, g_platform, o, j);
    char *out = NULL;
    if ((*e)->ExceptionCheck(e)) (*e)->ExceptionClear(e);
    else if (r)
    {
        const char *c = (*e)->GetStringUTFChars(e, r, NULL);
        out = strdup(c ? c : "");
        (*e)->ReleaseStringUTFChars(e, r, c);
        (*e)->DeleteLocalRef(e, r);
    }
    (*e)->DeleteLocalRef(e, o);
    (*e)->DeleteLocalRef(e, j);
    if (detach) (*g_vm)->DetachCurrentThread(g_vm);
    return out;
}

JNIEXPORT void JNICALL Java_io_github_recompcode_dbzbt4_RecompBridge_nativeInit(JNIEnv *e, jclass cls, jstring root, jstring server)
{
    g_cls = (jclass)(*e)->NewGlobalRef(e, cls);
    g_onEvent = (*e)->GetStaticMethodID(e, cls, "onEvent", "(Ljava/lang/String;Ljava/lang/String;)V");
    g_platform = (*e)->GetStaticMethodID(e, cls, "platform", "(Ljava/lang/String;Ljava/lang/String;)Ljava/lang/String;");
    const char *r = (*e)->GetStringUTFChars(e, root, NULL), *s = (*e)->GetStringUTFChars(e, server, NULL);
    goInit((char *)r, (char *)s);
    (*e)->ReleaseStringUTFChars(e, root, r);
    (*e)->ReleaseStringUTFChars(e, server, s);
}

JNIEXPORT jstring JNICALL Java_io_github_recompcode_dbzbt4_RecompBridge_nativeCall(JNIEnv *e, jclass cls, jstring method, jstring args)
{
    (void)cls;
    const char *m = (*e)->GetStringUTFChars(e, method, NULL), *a = (*e)->GetStringUTFChars(e, args, NULL);
    char *res = goCall((char *)m, (char *)a);
    (*e)->ReleaseStringUTFChars(e, method, m);
    (*e)->ReleaseStringUTFChars(e, args, a);
    jstring out = (*e)->NewStringUTF(e, res ? res : "{}");
    free(res);
    return out;
}
