package com.squaregolf.connector

import android.util.Log
import mobile.Connector
import mobile.Mobile
import mobile.StatusListener

/** Single logcat tag for the whole app. CLAUDE.md documents `adb logcat -s SquareGolf:V GoLog:V`. */
const val TAG = "SquareGolf"

/** Settings defaults. Declared exactly once; nothing else may spell these literals. */
const val DEFAULT_HOST = "127.0.0.1"
const val DEFAULT_PORT = 18921

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
    fun ensure(host: String, port: Int, listener: StatusListener): Connector {
        // bridge = null selects simulator mode. That IS Phase 1: no BLE, no BleBridge.
        val c = Mobile.newConnector(host, port, null, true, listener)
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
