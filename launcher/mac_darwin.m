// macOS helpers for the launcher (cgo, app_darwin.go): the launcher in front, the GS host brought
// to the front, the screen's work area, the Shift+Tab and Shift+M hotkeys (Carbon: no accessibility permission
// needed), the controller's Back / Start / Guide (GameController, also while another app is in front), and for the
// voice chat the right Option key (push to talk) and the microphone permission.
// AppKit only on the main thread: everything goes through onMain.

#import <Cocoa/Cocoa.h>
#import <Carbon/Carbon.h>
#import <GameController/GameController.h>
#import <AVFoundation/AVFoundation.h>
#include <libproc.h>

static void onMain(dispatch_block_t b)
{
	if ([NSThread isMainThread])
		b();
	else
		dispatch_sync(dispatch_get_main_queue(), b);
}

int rlInFront(void)
{
	__block int r = 0;
	onMain(^{
		NSWindow* w = NSApp.keyWindow;
		r = NSApp.isActive && w != nil && w.isVisible && !w.isMiniaturized;
	});
	return r;
}

// rlToFront: the launcher's window over everything, also over the game in full screen (its own Space)
void rlToFront(void)
{
	onMain(^{
		for (NSWindow* w in NSApp.windows)
		{
			if (!w.canBecomeKeyWindow)
				continue;
			w.collectionBehavior |= NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorFullScreenAuxiliary;
			if (w.isMiniaturized)
				[w deminiaturize:nil];
			[w makeKeyAndOrderFront:nil];
		}
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
		[NSApp activateIgnoringOtherApps:YES]; // [NSApp activate] may wait for the game to yield
#pragma clang diagnostic pop
	});
}

// rlFocusChildApp: the GS host (a child of the game runner) in front, with the keyboard and the controller
int rlFocusChildApp(int parent)
{
	pid_t kids[16];
	const int n = proc_listchildpids(parent, kids, sizeof kids); // a count of pids (not bytes, unlike proc_listpids)
	__block int done = 0;
	for (int i = 0; i < n && i < 16 && !done; i++)
	{
		const pid_t pid = kids[i];
		onMain(^{
			NSRunningApplication* app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
			if (!app)
				return;
			// macOS 14+ (cooperative activation): an app only comes to the front when the active one yields to it
			if (@available(macOS 14.0, *))
				[NSApp yieldActivationToApplication:app];
			done = [app activateWithOptions:NSApplicationActivateAllWindows];
		});
	}
	return done;
}

void rlWorkArea(int* w, int* h)
{
	__block NSRect r = NSMakeRect(0, 0, 1280, 800);
	onMain(^{
		if (NSScreen.mainScreen)
			r = NSScreen.mainScreen.visibleFrame;
	});
	*w = (int)r.size.width;
	*h = (int)r.size.height;
}

// ---- Shift+Tab (1) and Shift+M (2, the voice chat's microphone) ----

extern void goHotkey(int id);
static EventHotKeyRef s_hotkey, s_hotkeyMute;
static int s_handler;

static OSStatus hotkeyHandler(EventHandlerCallRef call, EventRef event, void* user)
{
	EventHotKeyID id = {0, 0};
	GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, NULL, sizeof id, NULL, &id);
	goHotkey((int)id.id);
	return noErr;
}

int rlHotkeyStart(void)
{
	__block int ok = 0;
	onMain(^{
		if (!s_handler)
		{
			EventTypeSpec t = {kEventClassKeyboard, kEventHotKeyPressed};
			InstallApplicationEventHandler(&hotkeyHandler, 1, &t, NULL, NULL);
			s_handler = 1;
		}
		if (!s_hotkey)
		{
			EventHotKeyID id = {'rcmp', 1};
			ok = RegisterEventHotKey(kVK_Tab, shiftKey, id, GetApplicationEventTarget(), 0, &s_hotkey) == noErr;
		}
		if (!s_hotkeyMute)
		{
			EventHotKeyID id = {'rcmp', 2};
			RegisterEventHotKey(kVK_ANSI_M, shiftKey, id, GetApplicationEventTarget(), 0, &s_hotkeyMute);
		}
	});
	return ok;
}

void rlHotkeyStop(void)
{
	onMain(^{
		if (s_hotkey)
		{
			UnregisterEventHotKey(s_hotkey);
			s_hotkey = NULL;
		}
		if (s_hotkeyMute)
		{
			UnregisterEventHotKey(s_hotkeyMute);
			s_hotkeyMute = NULL;
		}
	});
}

// ---- controller: XInput's bits (Start 0x10, Back 0x20, Guide 0x400) on any connected pad ----

unsigned rlPadButtons(void)
{
	static dispatch_once_t once;
	dispatch_once(&once, ^{
		onMain(^{
			if (@available(macOS 11.3, *))
				GCController.shouldMonitorBackgroundEvents = YES; // the game is in front while we watch
			(void)GCController.controllers;
		});
	});
	__block unsigned b = 0;
	onMain(^{
		for (GCController* c in GCController.controllers)
		{
			GCExtendedGamepad* g = c.extendedGamepad;
			if (!g)
				continue;
			if (g.buttonMenu.isPressed)
				b |= 0x0010;
			if (g.buttonOptions.isPressed)
				b |= 0x0020;
			if (g.buttonHome.isPressed)
				b |= 0x0400;
		}
	});
	return b;
}

// ---- voice chat ----

// rlRightOptionDown: push to talk. The modifier state is read, not hooked: no permission, and the game still gets
// the key.
int rlRightOptionDown(void)
{
	return (CGEventSourceFlagsState(kCGEventSourceStateHIDSystemState) & 0x40) != 0; // NX_DEVICERALTKEYMASK
}

// rlMicAccess: 0 not asked yet, 1 restricted, 2 denied, 3 allowed (AVAuthorizationStatus).
int rlMicAccess(void)
{
	return (int)[AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
}

extern void goMicAnswer(int granted);

// rlMicRequest: macOS asks the player once (Info.plist's NSMicrophoneUsageDescription says why).
void rlMicRequest(void)
{
	[AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {
		goMicAnswer(granted ? 1 : 0);
	}];
}
