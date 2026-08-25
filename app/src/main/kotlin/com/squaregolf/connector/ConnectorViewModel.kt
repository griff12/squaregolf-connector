package com.squaregolf.connector

import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CoroutineExceptionHandler
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import mobile.Mobile
import mobile.StatusListener

data class UiState(
    val nativeVersion: String = "loading…",
    val host: String = DEFAULT_HOST,
    val port: String = DEFAULT_PORT.toString(),
    val lmStatus: String = "disconnected",
    val gsproStatus: String = "disconnected",
    val armed: Boolean = false,
    val busy: Boolean = false,
    val phase: String = "idle",
    val elapsedSec: Int = 0,
    val lastShot: String = "",
    val error: String = "",
    val endpointInUse: String = "",
    val dropped: Long = 0,
    val lines: List<String> = emptyList(),
)

/**
 * Owns the connect/arm sequence and the status listener.
 *
 * Lives in a ViewModel, not the Activity: it survives configuration change, and
 * viewModelScope is not cancelled when a fold destroys and recreates the Activity.
 * The listener it installs never captures a composition or an Activity.
 */
class ConnectorViewModel : ViewModel() {

    private val _state = MutableStateFlow(UiState())
    val state: StateFlow<UiState> = _state.asStateFlow()

    /** Backstop so nothing reaches the default uncaught handler and kills the process. */
    private val crashGuard = CoroutineExceptionHandler { _, t ->
        Log.e(TAG, "uncaught in viewModelScope", t)
        _state.update { it.copy(busy = false, phase = "idle", error = describe(t)) }
    }

    /**
     * Invoked from a Go goroutine. Must not block and must not throw.
     *
     * MutableStateFlow.update is a lock-free CAS loop, so it is safe from any thread; the
     * UI observes it on the main thread via collectAsState. Every body is wrapped because
     * a throw here is caught by gobind and reported only as
     * "android engine: listener returned an error" in I/GoLog -- useful, but not a
     * substitute for handling it here.
     */
    private val listener = object : StatusListener {

        override fun onStatus(code: Int, detail: String) {
            try {
                Log.i(TAG, "status " + statusName(code) + "(" + code + ") " + detail.ifEmpty { "-" })
                append(("status " + statusName(code) + " " + detail).trim())
                _state.update { s ->
                    when (code) {
                        Mobile.StatusLMDisconnected -> s.copy(lmStatus = "disconnected")
                        Mobile.StatusLMScanning -> s.copy(lmStatus = "scanning")
                        Mobile.StatusLMConnecting -> s.copy(lmStatus = "connecting")
                        Mobile.StatusLMConnected -> s.copy(lmStatus = "connected")
                        Mobile.StatusLMError -> s.copy(lmStatus = "error", error = detail)
                        Mobile.StatusGSProDisconnected -> s.copy(gsproStatus = "disconnected")
                        Mobile.StatusGSProConnecting -> s.copy(gsproStatus = "connecting")
                        Mobile.StatusGSProConnected -> s.copy(gsproStatus = "connected")
                        Mobile.StatusGSProError -> s.copy(gsproStatus = "error", error = detail)
                        Mobile.StatusArmed -> s.copy(armed = true)
                        Mobile.StatusDisarmed -> s.copy(armed = false)
                        else -> s
                    }
                }
            } catch (t: Throwable) {
                Log.e(TAG, "onStatus", t)
            }
        }

        override fun onShot(
            ballSpeedMPS: Double,
            launchAngleDeg: Double,
            horizontalAngleDeg: Double,
            totalSpinRPM: Int,
            spinAxisDeg: Double,
        ) {
            try {
                val text = String.format(
                    "ball %.1f m/s   launch %.1f deg   horiz %.1f deg   spin %d rpm   axis %.1f deg",
                    ballSpeedMPS, launchAngleDeg, horizontalAngleDeg, totalSpinRPM, spinAxisDeg,
                )
                // CLAUDE.md: never log ball or club metrics above debug -- shot spam buries
                // the connection diagnostics you actually need when this breaks.
                Log.d(TAG, "shot " + text)
                _state.update { it.copy(lastShot = text) }
            } catch (t: Throwable) {
                Log.e(TAG, "onShot", t)
            }
        }

        override fun onLog(message: String) {
            try {
                Log.d(TAG, message)
                append(message)
            } catch (t: Throwable) {
                Log.e(TAG, "onLog", t)
            }
        }
    }

    init {
        viewModelScope.launch(crashGuard) {
            val v = withContext(Dispatchers.IO) {
                val version = Native.probe()
                // Displace any listener left installed by a previous ViewModel.
                Native.repointListener(listener)
                version
            }
            _state.update { it.copy(nativeVersion = v) }
        }
    }

    fun setHost(v: String) = _state.update { it.copy(host = v) }

    fun setPort(v: String) = _state.update { it.copy(port = v) }

    private var ticker: Job? = null

    /**
     * The whole Phase 1 gate, in one press.
     *
     * Order and the waits are both load-bearing. ConnectDevice only QUEUES the attempt --
     * bluetooth_manager sets status "scanning" synchronously and connects on its own
     * goroutine -- and Engine.Arm refuses unless the launch monitor is already Connected.
     * Firing the three calls back to back therefore always throws
     * "android engine: launch monitor is not connected", and the shot never happens.
     *
     * Each leg is guarded so a second press does not tear a live one down: ConnectDevice
     * unconditionally resets the launch-monitor status to "scanning".
     */
    fun fire() {
        if (_state.value.busy) return

        val host = _state.value.host.trim()
        val portText = _state.value.port.trim()
        if (host.isEmpty()) {
            _state.update { it.copy(error = "Host must not be empty") }
            return
        }
        val port = portText.toIntOrNull()
        if (port == null || port !in 1..65535) {
            _state.update { it.copy(error = "Port must be a number in 1..65535 (got \"" + portText + "\")") }
            return
        }

        _state.update { it.copy(busy = true, error = "", elapsedSec = 0, phase = "starting") }
        startTicker()

        viewModelScope.launch(crashGuard) {
            try {
                withContext(Dispatchers.IO) {
                    val c = Native.ensure(host, port, listener)

                    if (c.launchMonitorStatus() != "connected") {
                        phase("connecting launch monitor")
                        c.connectDevice()
                        await("launch monitor") { c.launchMonitorStatus() }
                    }

                    if (c.gsProStatus() != "connected") {
                        phase("connecting GSPro at " + host + ":" + port)
                        c.connectGSPro()
                        await("GSPro") { c.gsProStatus() }
                        Native.markConnected(host, port)
                    }

                    phase("arming")
                    c.arm()
                    phase("armed - first shot lands about 8.5 s from here")
                    _state.update {
                        it.copy(
                            endpointInUse = Native.endpointInUse(),
                            dropped = c.droppedNotifications(),
                        )
                    }
                }
            } catch (t: Throwable) {
                Log.e(TAG, "fire failed", t)
                _state.update { it.copy(error = describe(t), phase = "failed") }
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    /**
     * Disarm only.
     *
     * Deliberately NOT DisconnectGSPro and NOT Stop. Both close the TCP socket, and
     * GSPconnect never checks Read()'s return value: a client FIN makes it hot-spin
     * instead of re-accepting, and the NEXT connect then completes into the backlog and
     * appears to succeed while nothing flows. Recovering needs GSPconnect restarted via
     * connector.bat. Phase 1 has no reason to close the socket at all.
     */
    fun disarm() {
        viewModelScope.launch(crashGuard) {
            try {
                withContext(Dispatchers.IO) { Native.current()?.disarm() }
            } catch (t: Throwable) {
                Log.e(TAG, "disarm failed", t)
                _state.update { it.copy(error = describe(t)) }
            }
        }
    }

    fun clearError() = _state.update { it.copy(error = "") }

    // -----------------------------------------------------------------------
    // internals
    // -----------------------------------------------------------------------

    private fun phase(p: String) {
        Log.i(TAG, "phase: " + p)
        _state.update { it.copy(phase = p) }
    }

    /**
     * Polls a Go-side status string until it reads "connected".
     *
     * Polling rather than latching on a listener callback, because this is the same source
     * of truth Engine.Arm checks. The GSPro dial has a 5 s timeout and the plugin retries
     * with a 5 s initial backoff, so 30 s covers a couple of attempts before giving up
     * with a message that names the next diagnostic.
     */
    private suspend fun await(
        leg: String,
        timeoutMs: Long = 30_000L,
        status: () -> String,
    ) {
        val deadline = System.currentTimeMillis() + timeoutMs
        while (true) {
            val s = status()
            if (s == "connected") return
            if (s == "error") {
                val detail = Native.current()?.gsProError().orEmpty()
                throw IllegalStateException(
                    leg + " reported error: " + detail.ifEmpty { "(no detail)" }
                )
            }
            if (System.currentTimeMillis() > deadline) {
                throw IllegalStateException(
                    leg + " did not connect within " + (timeoutMs / 1000) + "s (last status \"" + s + "\"). " +
                        "If this is the GSPro leg: is GSPro Connect listening on 18921? " +
                        "Check: adb shell grep -i ':49E9 ' /proc/net/tcp"
                )
            }
            delay(200)
        }
    }

    private fun startTicker() {
        ticker?.cancel()
        ticker = viewModelScope.launch(crashGuard) {
            var n = 0
            while (true) {
                delay(1000)
                n += 1
                _state.update { it.copy(elapsedSec = n) }
                val st = _state.value
                if (!st.busy && !st.armed) break
            }
        }
    }

    private fun append(line: String) {
        if (line.isBlank()) return
        _state.update { s ->
            val next = s.lines + line
            s.copy(lines = if (next.size > 200) next.subList(next.size - 200, next.size) else next)
        }
    }

    private fun describe(t: Throwable): String =
        t.javaClass.simpleName + ": " + (t.message ?: "(no message)")

    private fun statusName(code: Int): String = when (code) {
        Mobile.StatusLMDisconnected -> "LM_DISCONNECTED"
        Mobile.StatusLMScanning -> "LM_SCANNING"
        Mobile.StatusLMConnecting -> "LM_CONNECTING"
        Mobile.StatusLMConnected -> "LM_CONNECTED"
        Mobile.StatusLMError -> "LM_ERROR"
        Mobile.StatusGSProDisconnected -> "GSPRO_DISCONNECTED"
        Mobile.StatusGSProConnecting -> "GSPRO_CONNECTING"
        Mobile.StatusGSProConnected -> "GSPRO_CONNECTED"
        Mobile.StatusGSProError -> "GSPRO_ERROR"
        Mobile.StatusArmed -> "ARMED"
        Mobile.StatusDisarmed -> "DISARMED"
        else -> "UNKNOWN"
    }
}
