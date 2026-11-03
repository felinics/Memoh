package ai.memoh.mobile;

import android.content.res.Configuration;
import android.graphics.Color;
import android.os.Build;
import android.os.Bundle;
import android.view.HapticFeedbackConstants;
import android.view.View;
import android.view.Window;
import android.webkit.JavascriptInterface;
import android.webkit.WebView;
import android.widget.Toast;
import androidx.activity.OnBackPressedCallback;
import androidx.core.content.ContextCompat;
import androidx.core.view.WindowCompat;
import com.getcapacitor.BridgeActivity;

/**
 * System bar and keyboard ownership:
 *
 * Capacitor 8 always registers its core SystemBars plugin. It owns the window insets listener
 * (pads the decor view by the IME height and injects --safe-area-inset-* CSS variables) and the
 * status/navigation bar icon style. This activity must not install a second insets listener or
 * write the icon appearance directly: SystemBars applies its style after onCreate and again on
 * every configuration change, so a second writer is silently overwritten (dark-mode phones then
 * get white icons on a light page), and a second IME padding doubles the keyboard gap.
 *
 * The page decides the theme and calls SystemBars.setStyle for the icons. This activity only
 * keeps the window background in sync with the page canvas, because SystemBars resets the decor
 * background to the theme's windowBackground on each setStyle and on configuration changes, and
 * that background is what shows in the IME padding while the keyboard animates.
 */
public class MainActivity extends BridgeActivity {

    private static final long EXIT_CONFIRM_WINDOW_MS = 1800;

    private long lastExitBackTime = 0;
    // Canvas color reported by the page; null until the page has applied its theme.
    private Integer pageCanvasColor = null;

    @Override
    public void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        Window window = getWindow();
        // Android 15+ enforces edge-to-edge for targetSdk 35+. Opting in here gives older
        // Android versions the same layout, so the page draws behind both system bars everywhere.
        WindowCompat.setDecorFitsSystemWindows(window, false);
        window.setStatusBarColor(Color.TRANSPARENT);
        window.setNavigationBarColor(Color.TRANSPARENT);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            // Keep the gesture bar transparent instead of a system-drawn translucent scrim.
            window.setNavigationBarContrastEnforced(false);
            window.setStatusBarContrastEnforced(false);
        }

        if (getBridge() == null || getBridge().getWebView() == null) {
            return;
        }
        final WebView webView = getBridge().getWebView();
        webView.setOverScrollMode(View.OVER_SCROLL_NEVER);
        // Until the page reports its canvas, match the system theme to avoid a white flash.
        webView.setBackgroundColor(ContextCompat.getColor(this, R.color.memoh_canvas));

        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                webView.evaluateJavascript(
                    "(function(){ return window.__handleNativeBack ? window.__handleNativeBack() : 'exit'; })()",
                    result -> {
                        if ("\"handled\"".equals(result)) {
                            return;
                        }
                        // Views the page did not claim (see apps/web/src/shells/mobile/
                        // native-bridge.ts) get browser history: back returns to the
                        // previous route.
                        if ("\"exit\"".equals(result) && webView.canGoBack()) {
                            webView.goBack();
                            return;
                        }
                        long now = System.currentTimeMillis();
                        if (now - lastExitBackTime < EXIT_CONFIRM_WINDOW_MS) {
                            moveTaskToBack(true);
                        } else {
                            lastExitBackTime = now;
                            Toast.makeText(MainActivity.this, R.string.back_again_to_exit, Toast.LENGTH_SHORT).show();
                        }
                    }
                );
            }
        });

        webView.addJavascriptInterface(new NativeBridge(webView), "MemohNative");
    }

    @Override
    public void onConfigurationChanged(Configuration newConfig) {
        // SystemBars handles this first and resets the decor background to windowBackground.
        super.onConfigurationChanged(newConfig);
        applyPageCanvas();

        WebView webView = getBridge() != null ? getBridge().getWebView() : null;
        if (webView != null) {
            boolean night = isSystemNight();
            webView.evaluateJavascript(
                "window.__onNativeNightModeChanged && window.__onNativeNightModeChanged(" + night + ")",
                null
            );
        }
    }

    private boolean isSystemNight() {
        int mode = getResources().getConfiguration().uiMode & Configuration.UI_MODE_NIGHT_MASK;
        return mode == Configuration.UI_MODE_NIGHT_YES;
    }

    private void applyPageCanvas() {
        if (pageCanvasColor == null) {
            return;
        }
        int color = pageCanvasColor;
        getWindow().getDecorView().setBackgroundColor(color);
        if (getBridge() != null && getBridge().getWebView() != null) {
            getBridge().getWebView().setBackgroundColor(color);
        }
    }

    private class NativeBridge {

        private final WebView webView;

        NativeBridge(WebView webView) {
            this.webView = webView;
        }

        @JavascriptInterface
        public boolean isSystemNight() {
            return MainActivity.this.isSystemNight();
        }

        /** Call after SystemBars.setStyle has resolved, since setStyle resets the decor background. */
        @JavascriptInterface
        public void setCanvasColor(final String hex) {
            final int color;
            try {
                color = Color.parseColor(hex);
            } catch (IllegalArgumentException e) {
                return;
            }
            runOnUiThread(() -> {
                pageCanvasColor = color;
                applyPageCanvas();
            });
        }

        /** "light" is a button tap (VIRTUAL_KEY), "heavy" a long press, "medium" a gesture threshold. */
        @JavascriptInterface
        public void trigger(final String style) {
            webView.post(() -> {
                int constant;
                if ("heavy".equals(style)) {
                    constant = HapticFeedbackConstants.LONG_PRESS;
                } else if ("medium".equals(style)) {
                    constant = HapticFeedbackConstants.CONTEXT_CLICK;
                } else {
                    constant = HapticFeedbackConstants.VIRTUAL_KEY;
                }
                webView.performHapticFeedback(constant, HapticFeedbackConstants.FLAG_IGNORE_VIEW_SETTING);
            });
        }
    }
}
