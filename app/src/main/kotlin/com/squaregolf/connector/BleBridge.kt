package com.squaregolf.connector

import android.annotation.SuppressLint
import android.bluetooth.BluetoothAdapter
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothGatt
import android.bluetooth.BluetoothGattCallback
import android.bluetooth.BluetoothGattCharacteristic
import android.bluetooth.BluetoothGattDescriptor
import android.bluetooth.BluetoothManager
import android.bluetooth.BluetoothProfile
import android.bluetooth.le.ScanCallback
import android.bluetooth.le.ScanResult
import android.bluetooth.le.ScanSettings
import android.content.Context
import android.os.Build
import android.util.Log
import java.util.UUID
import java.util.concurrent.CompletableFuture
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock

/**
 * Android BLE implementation of the Go-side `Bridge` contract (see
 * internal/transport/android/bridge.go, which documents every method).
 *
 * Three properties of this class are load-bearing and easy to get wrong.
 *
 * ONE OPERATION AT A TIME. Android's GATT stack allows exactly one outstanding
 * operation per connection; a second read/write/descriptor-write issued before the
 * previous callback lands is silently dropped. Every call therefore takes [opLock]
 * for its whole duration, including the wait for the callback.
 *
 * GO BLOCKS, ANDROID CALLS BACK. The Go transport calls these methods synchronously
 * from a goroutine and expects a result or an exception. Android answers on a binder
 * thread. Each operation parks on a [CompletableFuture] with a timeout, so a stack
 * that never calls back fails the call instead of parking a goroutine forever.
 *
 * NO PROTOCOL KNOWLEDGE. Per CLAUDE.md this class holds no UUIDs of its own, does not
 * parse notification bytes, and does not decide what anything means. UUID strings
 * arrive from Go (they originate in internal/core/constants.go) and packets go back
 * verbatim.
 */
@SuppressLint("MissingPermission") // callers gate on BlePermissions before constructing
class BleBridge(context: Context) : mobile.BleBridge {

    companion object {
        private const val TAG = "SquareGolfBLE"

        /** Client Characteristic Configuration Descriptor - the standard notification enable. */
        private val CCCD_UUID: UUID = UUID.fromString("00002902-0000-1000-8000-00805f9b34fb")

        private const val OP_TIMEOUT_MS = 10_000L
        private const val CONNECT_TIMEOUT_MS = 30_000L
        private const val SCAN_SETTLE_MS = 15_000L

        /** Android caps a peripheral's ATT MTU at 517; shot packets benefit from the headroom. */
        private const val DESIRED_MTU = 517

        private fun hex(bytes: ByteArray): String =
            bytes.joinToString("") { "%02x".format(it) }
    }

    /**
     * Set by [Native] once the Go connector exists. Callbacks are dropped until then,
     * which only happens in the window before the engine starts.
     */
    @Volatile
    var connector: mobile.Connector? = null

    private val appContext = context.applicationContext
    private val adapter: BluetoothAdapter? =
        (appContext.getSystemService(Context.BLUETOOTH_SERVICE) as? BluetoothManager)?.adapter

    /** Serialises GATT operations. Held across the callback wait, never across a scan. */
    private val opLock = ReentrantLock()

    @Volatile private var gatt: BluetoothGatt? = null
    @Volatile private var connectedAddress: String = ""

    // Pending completions. Only one can be live at a time because opLock is held.
    @Volatile private var pendingConnect: CompletableFuture<Unit>? = null
    @Volatile private var pendingRead: CompletableFuture<ByteArray>? = null
    @Volatile private var pendingWrite: CompletableFuture<Unit>? = null
    @Volatile private var pendingDescriptor: CompletableFuture<Unit>? = null
    @Volatile private var pendingMtu: CompletableFuture<Unit>? = null

    // Scanning
    private val scanLock = ReentrantLock()
    @Volatile private var scanning = false
    @Volatile private var scanPrefix = ""
    /** address -> device, for resolving a Connect target without rescanning. */
    private val seen = HashMap<String, BluetoothDevice>()
    /** address -> manufacturer-data hex, reported to Go on discovery. */
    private val seenMfg = HashMap<String, String>()

    // ---------------------------------------------------------------- scanning

    private val scanCallback = object : ScanCallback() {
        override fun onScanResult(callbackType: Int, result: ScanResult) {
            val device = result.device ?: return
            val name = result.scanRecord?.deviceName ?: device.name ?: return
            val prefix = scanPrefix
            // Android's ScanFilter.setDeviceName matches EXACTLY; upstream passes a
            // PREFIX, so the filter has to happen here. Reporting everything would
            // pay an FFI hop per stray advertisement.
            if (prefix.isNotEmpty() && !name.startsWith(prefix)) return

            // The first manufacturer-data record, hex-encoded, matching what
            // upstream's TinyGo client produces (hex.EncodeToString(mfgData[0].Data)).
            // If this is empty, DetectDeviceType silently resolves EVERY device as a
            // Home rather than an Omni - no error, just the wrong device profile.
            val mfg = result.scanRecord?.manufacturerSpecificData
            val mfgHex = if (mfg != null && mfg.size() > 0) hex(mfg.valueAt(0)) else ""
            if (mfgHex.isEmpty()) {
                Log.w(TAG, "no manufacturer data for $name; it will resolve as Home, not Omni")
            }

            scanLock.withLock {
                seen[device.address] = device
                seenMfg[device.address] = mfgHex
            }
            Log.d(TAG, "discovered $name (${device.address}) mfg=$mfgHex")
            connector?.onDeviceDiscovered(name, device.address, mfgHex)
        }

        override fun onScanFailed(errorCode: Int) {
            scanning = false
            Log.w(TAG, "scan failed, error $errorCode")
        }
    }

    override fun startScan(namePrefix: String) {
        val scanner = adapter?.bluetoothLeScanner
            ?: throw IllegalStateException("Bluetooth is off or unavailable")
        scanLock.withLock {
            scanPrefix = namePrefix
            if (scanning) return
            val settings = ScanSettings.Builder()
                .setScanMode(ScanSettings.SCAN_MODE_LOW_LATENCY)
                .build()
            // No ScanFilter: the filter is a prefix, which ScanFilter cannot express.
            scanner.startScan(null, settings, scanCallback)
            scanning = true
        }
        connector?.onConnectionStateChanged(1 /* ConnStateScanning */, "", "scanning for \"$namePrefix\"")
        Log.i(TAG, "scan started, prefix=\"$namePrefix\"")
    }

    override fun stopScan() {
        val scanner = adapter?.bluetoothLeScanner ?: return
        scanLock.withLock {
            if (!scanning) return
            scanner.stopScan(scanCallback)
            scanning = false
        }
        Log.i(TAG, "scan stopped")
    }

    // --------------------------------------------------------------- gatt core

    private val gattCallback = object : BluetoothGattCallback() {
        override fun onConnectionStateChange(g: BluetoothGatt, status: Int, newState: Int) {
            val address = g.device?.address ?: ""
            if (newState == BluetoothProfile.STATE_CONNECTED && status == BluetoothGatt.GATT_SUCCESS) {
                Log.i(TAG, "GATT connected to $address, discovering services")
                connector?.onConnectionStateChanged(2 /* Connecting */, address, "discovering services")
                if (!g.discoverServices()) {
                    failConnect(IllegalStateException("discoverServices() refused to start"))
                }
                return
            }

            // Anything else is a disconnect. status 133 is Android's catch-all GATT
            // error; it is usually a stale BluetoothGatt that was never closed.
            Log.w(TAG, "GATT disconnected from $address, status=$status newState=$newState")
            closeGatt()
            failAllPending(IllegalStateException("disconnected (GATT status $status)"))
            connectedAddress = ""
            connector?.onConnectionStateChanged(0 /* Disconnected */, address, "GATT status $status")
        }

        override fun onServicesDiscovered(g: BluetoothGatt, status: Int) {
            if (status != BluetoothGatt.GATT_SUCCESS) {
                failConnect(IllegalStateException("service discovery failed, status $status"))
                return
            }
            // Request a larger MTU before declaring the link ready. Shot data is
            // latency-sensitive and the 23-byte default fragments it. A refusal is
            // not fatal - onMtuChanged completes the future either way.
            if (!g.requestMtu(DESIRED_MTU)) {
                Log.w(TAG, "requestMtu refused to start; continuing at default MTU")
                pendingMtu?.complete(Unit)
            }
        }

        override fun onMtuChanged(g: BluetoothGatt, mtu: Int, status: Int) {
            Log.i(TAG, "MTU now $mtu (status $status)")
            pendingMtu?.complete(Unit)
        }

        override fun onCharacteristicRead(
            g: BluetoothGatt,
            ch: BluetoothGattCharacteristic,
            value: ByteArray,
            status: Int,
        ) {
            if (status == BluetoothGatt.GATT_SUCCESS) pendingRead?.complete(value.copyOf())
            else pendingRead?.completeExceptionally(
                IllegalStateException("read ${ch.uuid} failed, status $status")
            )
        }

        @Suppress("DEPRECATION")
        override fun onCharacteristicRead(
            g: BluetoothGatt,
            ch: BluetoothGattCharacteristic,
            status: Int,
        ) {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) return // newer overload fires
            if (status == BluetoothGatt.GATT_SUCCESS) pendingRead?.complete(ch.value?.copyOf() ?: ByteArray(0))
            else pendingRead?.completeExceptionally(
                IllegalStateException("read ${ch.uuid} failed, status $status")
            )
        }

        override fun onCharacteristicWrite(
            g: BluetoothGatt,
            ch: BluetoothGattCharacteristic,
            status: Int,
        ) {
            if (status == BluetoothGatt.GATT_SUCCESS) pendingWrite?.complete(Unit)
            else pendingWrite?.completeExceptionally(
                IllegalStateException("write ${ch.uuid} failed, status $status")
            )
        }

        override fun onDescriptorWrite(
            g: BluetoothGatt,
            descriptor: BluetoothGattDescriptor,
            status: Int,
        ) {
            if (status == BluetoothGatt.GATT_SUCCESS) pendingDescriptor?.complete(Unit)
            else pendingDescriptor?.completeExceptionally(
                IllegalStateException("descriptor write failed, status $status")
            )
        }

        override fun onCharacteristicChanged(
            g: BluetoothGatt,
            ch: BluetoothGattCharacteristic,
            value: ByteArray,
        ) {
            // Straight through to Go. No parsing here - CLAUDE.md keeps protocol
            // knowledge in Go, and Go copies the array on ingest.
            connector?.onNotification(ch.uuid.toString(), value)
        }

        @Suppress("DEPRECATION")
        override fun onCharacteristicChanged(g: BluetoothGatt, ch: BluetoothGattCharacteristic) {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) return // newer overload fires
            val v = ch.value ?: return
            connector?.onNotification(ch.uuid.toString(), v.copyOf())
        }
    }

    private fun failConnect(t: Throwable) {
        pendingMtu?.completeExceptionally(t)
        pendingConnect?.completeExceptionally(t)
    }

    private fun failAllPending(t: Throwable) {
        pendingConnect?.completeExceptionally(t)
        pendingMtu?.completeExceptionally(t)
        pendingRead?.completeExceptionally(t)
        pendingWrite?.completeExceptionally(t)
        pendingDescriptor?.completeExceptionally(t)
    }

    /**
     * Always close(), never merely disconnect(). A BluetoothGatt that is disconnected
     * but not closed keeps its client interface allocated; Android has a small fixed
     * pool of them, and exhausting it is the usual cause of status 133 on reconnect.
     */
    private fun closeGatt() {
        gatt?.let {
            try {
                it.close()
            } catch (t: Throwable) {
                Log.w(TAG, "error closing GATT: ${t.message}")
            }
        }
        gatt = null
    }

    private fun requireGatt(): BluetoothGatt =
        gatt ?: throw IllegalStateException("not connected")

    private fun findCharacteristic(uuid: String): BluetoothGattCharacteristic {
        val target = UUID.fromString(uuid)
        val g = requireGatt()
        for (service in g.services) {
            service.getCharacteristic(target)?.let { return it }
        }
        throw IllegalStateException("characteristic $uuid not found on this device")
    }

    private fun <T> await(future: CompletableFuture<T>, timeoutMs: Long, what: String): T =
        try {
            future.get(timeoutMs, TimeUnit.MILLISECONDS)
        } catch (e: TimeoutException) {
            throw IllegalStateException("$what timed out after ${timeoutMs}ms")
        } catch (e: java.util.concurrent.ExecutionException) {
            throw (e.cause as? Exception ?: IllegalStateException("$what failed: ${e.message}"))
        }

    // ------------------------------------------------------------ Bridge impl

    override fun connect(deviceName: String, deviceAddress: String, namePrefix: String) {
        val a = adapter ?: throw IllegalStateException("Bluetooth is off or unavailable")
        opLock.withLock {
            // Resolution order is defined by the Go contract: address, then exact
            // name, then first advertised name starting with the prefix.
            val device: BluetoothDevice = when {
                deviceAddress.isNotEmpty() -> a.getRemoteDevice(deviceAddress)
                else -> scanLock.withLock {
                    seen.values.firstOrNull { it.name == deviceName && deviceName.isNotEmpty() }
                        ?: seen.values.firstOrNull {
                            namePrefix.isNotEmpty() && it.name?.startsWith(namePrefix) == true
                        }
                        ?: throw IllegalStateException(
                            "no device matching name=\"$deviceName\" prefix=\"$namePrefix\"; scan first"
                        )
                }
            }

            // A previous GATT client must be closed before opening another, or the
            // next connect returns status 133.
            closeGatt()

            val connectFuture = CompletableFuture<Unit>()
            val mtuFuture = CompletableFuture<Unit>()
            pendingConnect = connectFuture
            pendingMtu = mtuFuture

            connector?.onConnectionStateChanged(2 /* Connecting */, device.address, "opening GATT")
            gatt = device.connectGatt(appContext, false, gattCallback, BluetoothDevice.TRANSPORT_LE)
                ?: throw IllegalStateException("connectGatt returned null")

            // Wait for services + MTU. The Go contract says Connect returns only when
            // the device is ready for reads and writes.
            await(mtuFuture, CONNECT_TIMEOUT_MS, "connect to ${device.address}")
            connectedAddress = device.address
            connectFuture.complete(Unit)

            // Report the advertisement BEFORE announcing Connected. Without this the
            // manufacturer data is unknown and upstream resolves the device as Home.
            val mfgHex = scanLock.withLock { seenMfg[device.address] } ?: ""
            val name = device.name ?: deviceName
            connector?.onDeviceDiscovered(name, device.address, mfgHex)
            connector?.onConnectionStateChanged(3 /* Connected */, device.address, "ready")
            Log.i(TAG, "connected to $name (${device.address})")
        }
    }

    override fun disconnect() {
        // Deliberately not under opLock: the contract requires Disconnect to be safe
        // to call concurrently and to make an in-flight operation fail fast rather
        // than wait behind it.
        val g = gatt
        connectedAddress = ""
        failAllPending(IllegalStateException("disconnected by request"))
        if (g == null) return
        try {
            g.disconnect()
        } catch (t: Throwable) {
            Log.w(TAG, "error during disconnect: ${t.message}")
        }
        closeGatt()
        Log.i(TAG, "disconnected")
    }

    override fun readCharacteristic(uuid: String): ByteArray = opLock.withLock {
        val ch = findCharacteristic(uuid)
        val f = CompletableFuture<ByteArray>()
        pendingRead = f
        if (!requireGatt().readCharacteristic(ch)) {
            pendingRead = null
            throw IllegalStateException("readCharacteristic($uuid) refused to start")
        }
        try {
            await(f, OP_TIMEOUT_MS, "read $uuid")
        } finally {
            pendingRead = null
        }
    }

    override fun writeCharacteristic(uuid: String, data: ByteArray) {
        opLock.withLock {
            val ch = findCharacteristic(uuid)
            val withResponse =
                (ch.properties and BluetoothGattCharacteristic.PROPERTY_WRITE) != 0
            val type = if (withResponse) {
                BluetoothGattCharacteristic.WRITE_TYPE_DEFAULT
            } else {
                BluetoothGattCharacteristic.WRITE_TYPE_NO_RESPONSE
            }
            val f = CompletableFuture<Unit>()
            pendingWrite = f
            val started = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                requireGatt().writeCharacteristic(ch, data, type) == BluetoothGatt.GATT_SUCCESS
            } else {
                @Suppress("DEPRECATION")
                run {
                    ch.writeType = type
                    ch.value = data
                    requireGatt().writeCharacteristic(ch)
                }
            }
            if (!started) {
                pendingWrite = null
                throw IllegalStateException("writeCharacteristic($uuid) refused to start")
            }
            try {
                await(f, OP_TIMEOUT_MS, "write $uuid")
            } finally {
                pendingWrite = null
            }
        }
    }

    override fun startNotifications(uuid: String) {
        opLock.withLock {
            val ch = findCharacteristic(uuid)
            val g = requireGatt()
            if (!g.setCharacteristicNotification(ch, true)) {
                throw IllegalStateException("setCharacteristicNotification($uuid) failed")
            }
            // Enabling notifications is TWO operations: the local flag above, then a
            // CCCD descriptor write on the wire. Skipping the second is the classic
            // "subscribed but no data ever arrives".
            val cccd = ch.getDescriptor(CCCD_UUID)
                ?: throw IllegalStateException("characteristic $uuid has no CCCD descriptor")
            val enable =
                if ((ch.properties and BluetoothGattCharacteristic.PROPERTY_NOTIFY) != 0) {
                    BluetoothGattDescriptor.ENABLE_NOTIFICATION_VALUE
                } else {
                    BluetoothGattDescriptor.ENABLE_INDICATION_VALUE
                }
            val f = CompletableFuture<Unit>()
            pendingDescriptor = f
            val started = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                g.writeDescriptor(cccd, enable) == BluetoothGatt.GATT_SUCCESS
            } else {
                @Suppress("DEPRECATION")
                run {
                    cccd.value = enable
                    g.writeDescriptor(cccd)
                }
            }
            if (!started) {
                pendingDescriptor = null
                throw IllegalStateException("CCCD write for $uuid refused to start")
            }
            try {
                await(f, OP_TIMEOUT_MS, "enable notifications on $uuid")
            } finally {
                pendingDescriptor = null
            }
            Log.i(TAG, "notifications enabled on $uuid")
        }
    }

    override fun stopNotifications(uuid: String) {
        opLock.withLock {
            val ch = try {
                findCharacteristic(uuid)
            } catch (t: Throwable) {
                return  // already gone; nothing to disable
            }
            val g = gatt ?: return
            g.setCharacteristicNotification(ch, false)
            val cccd = ch.getDescriptor(CCCD_UUID) ?: return
            val f = CompletableFuture<Unit>()
            pendingDescriptor = f
            val started = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                g.writeDescriptor(cccd, BluetoothGattDescriptor.DISABLE_NOTIFICATION_VALUE) ==
                    BluetoothGatt.GATT_SUCCESS
            } else {
                @Suppress("DEPRECATION")
                run {
                    cccd.value = BluetoothGattDescriptor.DISABLE_NOTIFICATION_VALUE
                    g.writeDescriptor(cccd)
                }
            }
            if (started) {
                try {
                    await(f, OP_TIMEOUT_MS, "disable notifications on $uuid")
                } catch (t: Throwable) {
                    Log.w(TAG, "disable notifications on $uuid: ${t.message}")
                } finally {
                    pendingDescriptor = null
                }
            } else {
                pendingDescriptor = null
            }
        }
    }
}
