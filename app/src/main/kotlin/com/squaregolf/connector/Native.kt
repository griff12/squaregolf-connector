package com.squaregolf.connector

import android.content.Context
import android.util.Log
import mobile.Connector
import mobile.Mobile
import mobile.StatusListener

/** Single logcat tag for the whole app. CLAUDE.md documents `adb logcat -s SquareGolf:V GoLog:V`. */
const val TAG = "SquareGolf"

/** Settings defaults. Declared exactly once; nothing else may spell these literals. */
/** The PC running GSPro Connect. Must be changed by the user - there is no sane default. */
const val DEFAULT_HOST = "192.168.1.10"
/** GSPro Open Connect. 921 is the documented port and binds normally on Windows. */
const val DEFAULT_PORT = 921

/**
 * Process-wide owner of the one [Connector].
 *
 * The Go engine is a process singleton: `Mobile.newConnector` re-attaches to the existing
 * engine and only re-points the listener and the endpoint. Holding the Connector here --
 * rather than in an Activity or a composition -- means an Activity recreation (every fold
 * of a Z Fold) neither loses the live connection nor leaves a dead listener installed.
 *
 * Everything here is blocking JNI. Call it off the main thread.
 */
object Native {

    @Volatile
    private var connector: Connector? = null

    @Volatile
    private var connectedEndpoint: String = ""

    /** Application context, handed over once by MainActivity so the ViewModel needs none. */
    @Volatile
    private var appContext: Context? = null

    /**
     * The one BleBridge. Process-scoped for the same reason the Connector is: a fold
     * must not drop the GATT link or strand the Go side pointing at a dead bridge.
     */
    @Volatile
    private var bridge: BleBridge? = null

    @Synchronized
    fun attachContext(context: Context) {
        if (appContext == null) appContext = context.applicationContext
    }

    /** Null before [attachContext], which cannot happen once MainActivity has started. */
    @Synchronized
    fun bleBridge(): BleBridge? {
        val ctx = appContext ?: return null
        return bridge ?: BleBridge(ctx).also { bridge = it }
    }

    /** Cached result of [Mobile.version], or the load failure rendered as text. */
    @Volatile
    var nativeVersion: String = ""
        private set

    /**
     * Touches the native library and caches its version string.
     *
     * A non-empty, non-"FAILED" result proves System.loadLibrary("gojni") succeeded,
     * Java_go_Seq_init ran, and the JNI table is bound -- i.e. it collapses the whole
     * "did the .so load" question into one label.
     *
     * Never throws. A native load failure surfaces as ExceptionInInitializerError from
     * go.Seq's static initialiser, and every *subsequent* touch degrades to a causeless
     * NoClassDefFoundError, so the first one must be captured.
     */
    fun probe(): String {
        nativeVersion.takeIf { it.isNotEmpty() }?.let { return it }
        val v = try {
            Mobile.version().ifEmpty { "(empty)" }
        } catch (t: Throwable) {
            Log.e(TAG, "native load failed", t)
            "NATIVE LOAD FAILED: ${t.javaClass.simpleName}: ${t.message}"
        }
        nativeVersion = v
        return v
    }

    /**
     * Returns the process Connector, creating it on first call and re-pointing its
     * listener and endpoint on every later call. Performs no I/O.
     */
    @Synchronized
    fun ensure(
        host: String,
        port: Int,
        listener: StatusListener,
        bridge: BleBridge? = null,
    ): Connector {
        // A null bridge selects simulator mode (Phase 1). A real bridge drives the
        // radio (Phase 2). simulateOmni only matters to the simulator, so it tracks
        // whichever of the two is in play.
        val c = Mobile.newConnector(host, port, bridge, bridge == null, listener)
        // The bridge needs the Connector to deliver notifications and state changes
        // back into Go. newConnector performs no I/O, so nothing can call into the
        // bridge before this assignment lands.
        bridge?.connector = c
        connector = c
        return c
    }

    /** Re-points the listener if a Connector already exists. No-op before the first [ensure]. */
    @Synchronized
    fun repointListener(listener: StatusListener) {
        connector?.setListener(listener)
    }

    @Synchronized
    fun current(): Connector? = connector

    @Synchronized
    fun markConnected(host: String, port: Int) {
        connectedEndpoint = "$host:$port"
    }

    @Synchronized
    fun endpointInUse(): String = connectedEndpoint
}
