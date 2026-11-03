# Mobile Shell (apps/mobile)

`@memohai/mobile` is the native mobile shell: a [Capacitor](https://capacitorjs.com) app that loads the Memoh web app (`apps/web`) in a system WebView. It owns what only a native app can do. Pages, navigation and everything the user sees belong to `apps/web`.

## Boundary

The shell owns:

- system back (the Android back gesture and button);
- the status bar and gesture bar: drawing under them, icon style, window background;
- haptics and other device capabilities exposed to the page;
- packaging, app id, name, icons and splash.

The shell must not contain pages or app UI, and `apps/web` must not be forked for mobile. Mobile layout and navigation live in `apps/web/src/shells/mobile/`; see the "Layout System" section of `apps/web/AGENTS.md` for where each kind of mobile difference goes.

Everything the shell and the page ask of each other goes through one module on the web side, `apps/web/src/shells/mobile/native-bridge.ts`, which documents the contract. Change both sides together.

Current contract:

- **System back.** `MainActivity` evaluates `window.__handleNativeBack()`. `'handled'` means the page consumed the back. `'exit'` falls back to WebView history, and with no history to a double-back exit with a native toast.
- **System bars.** On every theme change the page calls Capacitor's `SystemBars.setStyle` for the icon style, then `MemohNative.setCanvasColor` for the window background, in that order, because `setStyle` resets the background.
- **`window.MemohNative`** also exposes `isSystemNight` and `trigger` (haptics), which the web app does not use yet.

## Server address

The shell loads the web app from a server instead of bundling it, so web changes reach an installed build on reload without reinstalling. The address comes from `MEMOH_MOBILE_DEV_URL` when `cap sync` runs and is written into the Android project. Changing it needs a new sync and build. Without it the shell shows `www/index.html`, which only explains how to set it.

A phone reaches a dev server over the LAN, so the server must listen on all interfaces (for example `pnpm --filter @memohai/web dev --host 0.0.0.0`). Plain `http` addresses are allowed for this.

## Android

Requires JDK 21 and the Android SDK. Point Gradle at the SDK with `ANDROID_HOME` or `android/local.properties` (`sdk.dir=…`, not committed).

```bash
MEMOH_MOBILE_DEV_URL=http://<lan-ip>:<port> pnpm --filter @memohai/mobile android:build
# APK: android/app/build/outputs/apk/debug/app-debug.apk
```

- Debug builds use the id `ai.memoh.mobile.dev` and the name "Memoh Dev" (`android/app/src/debug/res`), so they install beside a release build.
- `cap sync` regenerates `android/capacitor.settings.gradle`, `android/app/capacitor.build.gradle` and the ignored `capacitor-cordova-android-plugins/` and `app/src/main/assets/` copies. Commit the two Gradle files when they change; do not hand-edit them.

### Emulator

An emulator reaches the dev server on the host's LAN address, the same URL a phone uses. Set it to 360dp wide to match common Android phones, and let the on-screen keyboard appear even though the emulator has a hardware keyboard:

```bash
adb shell wm density 480                                  # 1080px wide panel -> 360dp
adb shell settings put secure show_ime_with_hard_keyboard 1
adb install -r android/app/build/outputs/apk/debug/app-debug.apk
adb shell cmd uimode night yes|no                         # switch the system theme
```

Check the WebView version (`adb shell dumpsys package com.google.android.webview | grep versionName`): below 140 Capacitor pads the window natively instead of letting the page draw under the system bars, so test both an old and a current WebView when changing insets.

iOS is not set up yet.
