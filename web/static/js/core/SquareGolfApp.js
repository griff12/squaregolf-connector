// core/SquareGolfApp.js
import { EventBus } from './EventBus.js';
import { WebSocketService } from '../services/WebSocketService.js';
import { DeviceService } from '../services/DeviceService.js';
import { ApiClient } from '../services/ApiClient.js';
import { AlignmentManager } from '../features/AlignmentManager.js';
import { SettingsManager } from '../features/SettingsManager.js';
import { IntegrationsManager } from '../features/IntegrationsManager.js';
import { ShotMonitor } from '../features/ShotMonitor.js';
import { ToastManager } from '../ui/ToastManager.js';
import { ScreenManager } from '../ui/ScreenManager.js';

export class SquareGolfApp {
    constructor() {
        // Core infrastructure
        this.eventBus = new EventBus();
        this.api = new ApiClient();

        // UI managers
        this.toast = new ToastManager();
        this.screen = new ScreenManager(this.eventBus);

        // Services
        this.ws = new WebSocketService(this.eventBus);
        this.deviceService = new DeviceService(this.api, this.eventBus);

        // Features
        this.alignmentManager = new AlignmentManager(this.api, this.eventBus);
        this.settingsManager = new SettingsManager(this.api, this.eventBus);
        this.integrationsManager = new IntegrationsManager(this.api, this.toast);
        this.shotMonitor = new ShotMonitor(this.api, this.eventBus);

        // Local state
        this.features = {};
        this.currentHandedness = 'right';
        this.alignmentExplicitlyStopped = false;
        this.alignmentPanelClosing = false;
        this.alignmentInFlight = false;
        this.pendingDeviceAction = null;
        this.deviceConnected = false;
        this.alignmentCloseDelayMs = 300;

        this.init();
    }

    $(id) {
        return document.getElementById(id);
    }

    $$(selector) {
        return [...document.querySelectorAll(selector)];
    }

    bind(id, eventName, handler) {
        this.$(id)?.addEventListener(eventName, handler);
    }

    init() {
        this.loadFeatures().then(() => {
            this.setupEventListeners();
            this.setupEventBusListeners();
            this.setHidden(this.$('statusBar'), true);
            this.ws.connect();
            this.settingsManager.load();
            this.integrationsManager.init();
        });
    }

    setupEventBusListeners() {
        // WebSocket events
        this.eventBus.on('ws:connected', () => this.updateConnectionIndicator(true));
        this.eventBus.on('ws:disconnected', () => this.updateConnectionIndicator(false));
        this.eventBus.on('ws:error', () => this.updateConnectionIndicator(false));
        this.eventBus.on('ws:message', (msg) => this.handleWebSocketMessage(msg));

        // Device events
        this.eventBus.on('device:connecting', () => {
            this.toast.info('Connection initiated...');
            this.pendingDeviceAction = 'manual-connect';
        });
        this.eventBus.on('device:disconnecting', () => {
            this.toast.info('Disconnection initiated...');
            this.pendingDeviceAction = 'manual-disconnect';
        });
        this.eventBus.on('device:error', (msg) => this.toast.error(`Connection failed: ${msg}`));
        this.eventBus.on('device:status', (status) => this.updateDeviceStatus(status));

        // Alignment events
        this.eventBus.on('alignment:saved', () => {
            this.setAlignmentBusy(false);
            this.toast.success('Calibration saved');
            this.setAlignmentError('');
            this.updateAlignmentDisplay(0, false);
            this.closeAlignmentPanel();
        });
        this.eventBus.on('alignment:cancelled', () => {
            this.setAlignmentBusy(false);
            this.toast.info('Calibration cancelled');
            this.setAlignmentError('');
            this.updateAlignmentDisplay(0, false);
            this.closeAlignmentPanel();
        });
        this.eventBus.on('alignment:error', (msg) => {
            this.setAlignmentBusy(false);
            this.setAlignmentError(msg);
            this.toast.error(msg);
        });
        this.eventBus.on('alignment:update', ({ angle, isAligned }) => {
            this.updateAlignmentDisplay(angle, isAligned);
        });
        this.eventBus.on('alignment:started', () => this.setAlignmentBusy(false));
        this.eventBus.on('alignment:stopped', () => this.setAlignmentBusy(false));
        this.eventBus.on('alignment:handedness-changed', (handedness) => {
            this.currentHandedness = handedness;
            this.updateHandednessDisplay(handedness);
        });

        // Screen navigation events
        this.eventBus.on('screen:before-change', ({ from, to }) => {
            // Close alignment panel if leaving device screen
            if (from === 'device' && to !== 'device') {
                this.closeAlignmentPanel();
            }
        });
        this.eventBus.on('screen:changed', (screenName) => {
            const statusBar = this.$('statusBar');
            if (statusBar) {
                this.setHidden(statusBar, screenName === 'device');
            }
        });

        // Settings events
        this.eventBus.on('settings:loaded', (settings) => this.applySettings(settings));
        this.eventBus.on('settings:error', (msg) => this.toast.error(`Failed to save settings: ${msg}`));
    }

    setupEventListeners() {
        // Navigation
        this.screen.navButtons.forEach((button) => {
            button.addEventListener('click', ({ currentTarget }) => {
                this.screen.show(currentTarget.dataset.screen);
            });
        });

        // Status bar navigation
        this.bind('statusDevice', 'click', () => this.screen.show('device'));
        this.bind('statusBallReady', 'click', () => this.screen.show('device'));

        // Alignment panel controls
        this.bind('calibrateBtn', 'click', () => this.openAlignmentPanel());
        this.bind('closeAlignmentBtn', 'click', () => this.closeAlignmentPanel());
        this.bind('retryAlignmentBtn', 'click', () => this.retryAlignment());

        // Device controls
        this.bind('connectDisconnectBtn', 'click', () => {
            if (this.deviceConnected) {
                this.pendingDeviceAction = 'manual-disconnect';
                this.deviceService.disconnect();
            } else {
                this.pendingDeviceAction = 'manual-connect';
                this.deviceService.connect('');
            }
        });

        // Integrations are rendered and wired by IntegrationsManager from the
        // backend manifest — no per-integration controls here.

        // Alignment controls
        this.bind('leftHandedBtn', 'click', () => this.handleHandednessChange('left'));
        this.bind('rightHandedBtn', 'click', () => this.handleHandednessChange('right'));
        this.bind('saveAlignmentBtn', 'click', () => {
            this.alignmentExplicitlyStopped = true;
            this.alignmentManager.save();
        });
        this.bind('cancelAlignmentBtn', 'click', () => {
            this.alignmentExplicitlyStopped = true;
            this.alignmentManager.cancel();
        });

        // Settings controls
        this.$$('input[name="spinMode"]').forEach((radio) => {
            radio.addEventListener('change', () => this.saveSettings());
        });
        this.bind('omniSpeedUnit', 'change', () => this.saveSettings());
        this.bind('omniDistanceUnit', 'change', () => this.saveSettings());
        this.bind('omniGreenSpeed', 'change', () => this.saveSettings());
        this.bind('omniCarryAdjustment', 'change', () => this.saveSettings());
    }

    async handleHandednessChange(handedness) {
        const result = await this.alignmentManager.setHandedness(handedness);

        if (result.success && this.$('alignmentPanel')?.classList.contains('open')) {
            // Restart alignment with new handedness
            await this.alignmentManager.stop();
            await new Promise(resolve => setTimeout(resolve, 100));
            await this.alignmentManager.start();
        }
    }

    handleWebSocketMessage(message) {
        switch (message.type) {
            case 'deviceStatus':
                this.deviceService.updateStatus(message.data);
                break;
            case 'integrationStatus':
                this.integrationsManager.updateStatus(message.data);
                break;
            case 'alignmentData':
                if (message.data) {
                    this.alignmentManager.updateDisplay(
                        message.data.alignmentAngle || 0,
                        message.data.isAligned || false
                    );
                }
                break;
            default:
                console.log('Unknown WebSocket message type:', message.type);
        }
    }

    updateConnectionIndicator(connected) {
        this.updateBinaryIndicator('statusWebSocket', connected);
    }

    updateDeviceConnectionIndicator(deviceStatus) {
        this.updateBinaryIndicator('statusDevice', deviceStatus === 'connected');
    }

    setHidden(element, shouldHide) {
        if (!element) return;
        element.classList.toggle('hidden', shouldHide);
    }

    updateBinaryIndicator(elementId, isConnected) {
        const element = this.$(elementId);
        if (!element) return;

        element.classList.toggle('connected', isConnected);
        element.classList.toggle('disconnected', !isConnected);
    }

    updateGlobalConnectionIndicator(elementId, connectionStatus) {
        this.updateBinaryIndicator(elementId, connectionStatus === 'connected');
    }

    updateConnectionPanel({
        status,
        statusElementId,
        errorElementId,
        connectBtnId,
        disconnectBtnId,
        ipFieldId,
        portFieldId
    }) {
        const statusElement = this.$(statusElementId);
        const errorElement = this.$(errorElementId);
        const connectBtn = this.$(connectBtnId);
        const disconnectBtn = this.$(disconnectBtnId);
        const ipField = this.$(ipFieldId);
        const portField = this.$(portFieldId);

        if (statusElement) {
            statusElement.className = 'status-value';
            statusElement.classList.add(status.connectionStatus);
        }

        const isConnected = status.connectionStatus === 'connected';
        const isConnecting = status.connectionStatus === 'connecting';
        const isDisconnected = status.connectionStatus === 'disconnected';
        const isError = status.connectionStatus === 'error';
        const statusText = {
            connected: 'Connected',
            connecting: 'Connecting...',
            disconnected: 'Disconnected',
            error: 'Error'
        };

        if (statusElement) {
            statusElement.textContent = statusText[status.connectionStatus] || 'Disconnected';
        }

        if (connectBtn) connectBtn.disabled = isConnected || isConnecting;
        if (disconnectBtn) disconnectBtn.disabled = !isConnected;
        if (ipField) ipField.disabled = isConnected || isConnecting;
        if (portField) portField.disabled = isConnected || isConnecting;

        if (errorElement) {
            if (isError && status.lastError) {
                errorElement.textContent = status.lastError;
                this.setHidden(errorElement, false);
            } else {
                this.setHidden(errorElement, true);
            }
        }
    }

    updateDeviceControls({ canConnect, canDisconnect, showCalibrate, showDeviceInfo, errorMessage = '' }) {
        const btn = this.$('connectDisconnectBtn');
        const calibrateBtn = this.$('calibrateBtn');
        const deviceDetailsInline = this.$('deviceDetailsInline');
        const deviceHeaderSeparator = this.$('deviceHeaderSeparator');
        const batteryInline = this.$('batteryInline');
        const errorElement = this.$('deviceError');

        this.deviceConnected = canDisconnect;

        if (btn) {
            btn.textContent = canDisconnect ? 'Disconnect' : 'Connect';
            btn.disabled = !canConnect && !canDisconnect;
        }

        this.setHidden(calibrateBtn, !showCalibrate);
        this.setHidden(deviceDetailsInline, !showDeviceInfo);
        this.setHidden(deviceHeaderSeparator, !showDeviceInfo);
        this.setHidden(batteryInline, !showDeviceInfo);

        if (errorElement) {
            if (errorMessage) {
                errorElement.textContent = errorMessage;
                this.setHidden(errorElement, false);
            } else {
                this.setHidden(errorElement, true);
            }
        }
    }

    showSlidingPanel(panelId, overlayId) {
        const panel = this.$(panelId);
        const overlay = this.$(overlayId);

        if (panel) {
            panel.classList.remove('hidden');
        }

        if (overlay) {
            overlay.classList.remove('hidden');
        }

        requestAnimationFrame(() => {
            if (panel) {
                panel.classList.add('open');
            }
            if (overlay) {
                overlay.classList.add('open');
            }
        });

        return { panel, overlay };
    }

    hideSlidingPanel(panelId, overlayId, delayMs) {
        const panel = this.$(panelId);
        const overlay = this.$(overlayId);
        const finalizeHide = (element) => {
            this.setHidden(element, true);
        };
        const hideAfterTransition = (element) => {
            if (!element) return;

            let finalized = false;
            const complete = () => {
                if (finalized) return;
                finalized = true;
                element.removeEventListener('transitionend', onTransitionEnd);
                finalizeHide(element);
            };
            const onTransitionEnd = (event) => {
                if (event.target !== element) return;
                complete();
            };

            element.addEventListener('transitionend', onTransitionEnd);
            window.setTimeout(complete, delayMs + 50);
        };

        if (panel) {
            panel.classList.remove('open');
            hideAfterTransition(panel);
        }

        if (overlay) {
            overlay.classList.remove('open');
            hideAfterTransition(overlay);
        }
    }

    setTextContent(elementId, value, fallback = '-') {
        const element = this.$(elementId);
        if (element) {
            element.textContent = value ?? fallback;
        }
    }

    updateOptionalDeviceInfo({ itemId, valueId, value, formatter = (entry) => entry }) {
        const itemElement = this.$(itemId);
        const valueElement = this.$(valueId);
        const hasValue = value !== null && value !== undefined && value !== '';

        this.setHidden(itemElement, !hasValue);
        if (valueElement) {
            valueElement.textContent = hasValue ? formatter(value) : '-';
        }
    }

    updateBatteryDisplay(level, chargingStatus) {
        const batteryElement = this.$('batteryLevel');
        const batteryIconElement = this.$('batteryIcon');
        if (!batteryElement || !batteryIconElement) return;

        if (typeof level !== 'number') {
            batteryIconElement.textContent = 'battery_unknown';
            batteryElement.textContent = '—';
            batteryIconElement.classList.remove('battery-low', 'battery-charging');
            return;
        }

        // chargingStatus: 0=not charging, 1=discharging, 2=charging, 3=full, 5=AC powered
        if (chargingStatus === 5) {
            batteryIconElement.textContent = 'power';
            batteryElement.textContent = 'AC';
            batteryIconElement.classList.remove('battery-low');
            batteryIconElement.classList.remove('battery-charging');
            return;
        }

        if (chargingStatus === 2) {
            batteryIconElement.textContent = 'battery_charging_full';
            batteryElement.textContent = `${level}%`;
            batteryIconElement.classList.remove('battery-low');
            batteryIconElement.classList.add('battery-charging');
            return;
        }

        batteryIconElement.classList.remove('battery-charging');

        if (level >= 80) {
            batteryIconElement.textContent = 'battery_full';
        } else if (level >= 50) {
            batteryIconElement.textContent = 'battery_3_bar';
        } else if (level >= 20) {
            batteryIconElement.textContent = 'battery_2_bar';
        } else {
            batteryIconElement.textContent = 'battery_1_bar';
        }

        batteryIconElement.classList.toggle('battery-low', level <= 5);
        batteryElement.textContent = level <= 5 ? 'Low' : `${level}%`;
    }

    updateCapacitorDisplay(ready, connectionStatus) {
        const el = this.$('capacitorStatus');
        if (!el) return;

        if (connectionStatus !== 'connected') {
            el.textContent = '';
            el.classList.remove('capacitor-charging', 'capacitor-ready');
            return;
        }

        if (ready) {
            el.textContent = 'Ready';
            el.classList.remove('capacitor-charging');
            el.classList.add('capacitor-ready');
        } else {
            el.textContent = 'Charging...';
            el.classList.add('capacitor-charging');
            el.classList.remove('capacitor-ready');
        }
    }

    updateVersionDisplay(status) {
        const modelNames = { home: 'Home', omni: 'Omni', unknown: null };
        this.setTextContent('deviceModel', modelNames[status.deviceType] ?? null);
        this.setTextContent('firmwareVersion', status.firmwareVersion !== null ? status.firmwareVersion : null);
        this.setTextContent('launcherVersion', status.launcherVersion !== null ? status.launcherVersion : null);
        this.setTextContent('mmiVersion', status.mmiVersion !== null ? status.mmiVersion : null);
        this.setTextContent('launchMonitorStatus', status.launchMonitorStatus ? `${status.launchMonitorStatus}` : null);
    }

    formatOmniStatus(value) {
        const names = {
            0: 'None',
            1: 'Idle',
            2: 'Init',
            3: 'Detect',
            4: 'Ready',
            5: 'Shot',
            6: 'Done'
        };

        if (typeof value !== 'number') return null;
        return names[value] ? `${value}:${names[value]}` : `${value}`;
    }

    updateDeviceStatus(status) {
        // Update the main navigation device connection indicator
        this.updateDeviceConnectionIndicator(status.connectionStatus);
        this.updateDeviceHeaderStatus(status);

        switch (status.connectionStatus) {
            case 'connected':
                this.updateDeviceControls({
                    canConnect: false,
                    canDisconnect: true,
                    showCalibrate: true,
                    showDeviceInfo: true
                });
                this.pendingDeviceAction = null;
                break;
            case 'scanning':
                this.updateDeviceControls({
                    canConnect: false,
                    canDisconnect: true,
                    showCalibrate: false,
                    showDeviceInfo: false
                });
                break;
            case 'connecting':
                this.updateDeviceControls({
                    canConnect: false,
                    canDisconnect: true,
                    showCalibrate: false,
                    showDeviceInfo: false
                });
                break;
            case 'disconnected':
                this.updateDeviceControls({
                    canConnect: true,
                    canDisconnect: false,
                    showCalibrate: false,
                    showDeviceInfo: false
                });
                this.pendingDeviceAction = null;
                break;
            case 'error':
                this.updateDeviceControls({
                    canConnect: true,
                    canDisconnect: true,
                    showCalibrate: false,
                    showDeviceInfo: false,
                    errorMessage: status.lastError || ''
                });
                this.pendingDeviceAction = null;
                break;
        }

        this.updateBatteryDisplay(status.batteryLevel, status.batteryCharging);
        this.updateCapacitorDisplay(status.capacitorReady, status.connectionStatus);
        this.updateVersionDisplay(status);
        this.updateOptionalDeviceInfo({
            itemId: 'clubItem',
            valueId: 'clubValue',
            value: status.club,
            formatter: (club) => club.regularCode || club.name
        });

        const handedness = status.handedness === null ? null : (status.handedness === 0 ? 'Right' : 'Left');
        this.updateOptionalDeviceInfo({
            itemId: 'handednessItem',
            valueId: 'handednessValue',
            value: handedness
        });

        this.updateOptionalDeviceInfo({
            itemId: 'omniStatusItem',
            valueId: 'omniStatusValue',
            value: status.deviceType === 'omni' ? status.omniStatus : null,
            formatter: (value) => this.formatOmniStatus(value)
        });

        this.updateOptionalDeviceInfo({
            itemId: 'omniHomeStatusItem',
            valueId: 'omniHomeStatusValue',
            value: status.deviceType === 'omni' ? status.omniHomeGolfStatus : null,
            formatter: (value) => this.formatOmniStatus(value)
        });

        this.updateOptionalDeviceInfo({
            itemId: 'omniClubSelectionItem',
            valueId: 'omniClubSelectionValue',
            value: status.deviceType === 'omni' ? status.omniClubSelection : null
        });

        this.updateOptionalDeviceInfo({
            itemId: 'omniSensorStatusItem',
            valueId: 'omniSensorStatusValue',
            value: status.deviceType === 'omni' ? status.omniSensorStatus : null
        });

        const omniSettings = this.$('omniSettingsGroup');
        if (omniSettings) {
            omniSettings.classList.toggle('hidden', status.deviceType !== 'omni');
        }

        if (handedness) {
            this.currentHandedness = handedness.toLowerCase();
            this.updateHandednessDisplay(this.currentHandedness);
        }

        // Update Shot Monitor
        this.shotMonitor.updateStatus(status);

        // If we have new shot data, update current shot
        if (status.lastBallMetrics && Object.keys(status.lastBallMetrics).length > 0) {
            this.shotMonitor.updateCurrentShot(status.lastBallMetrics, status.lastClubMetrics);
        }

        // Update alignment display if alignment data is present
        if (status.isAligning && typeof status.alignmentAngle === 'number') {
            this.updateAlignmentDisplay(status.alignmentAngle, status.isAligned || false);
        }
    }

    openAlignmentPanel() {
        if (this.alignmentInFlight || this.$('alignmentPanel')?.classList.contains('open')) {
            return;
        }

        this.setAlignmentError('');
        const { overlay } = this.showSlidingPanel('alignmentPanel', 'alignmentOverlay');

        if (overlay) {
            overlay.addEventListener('click', () => this.closeAlignmentPanel(), { once: true });
        }

        this.setAlignmentBusy(true);
        this.alignmentManager.start();
    }

    closeAlignmentPanel() {
        if (this.alignmentPanelClosing) return;
        this.alignmentPanelClosing = true;
        this.hideSlidingPanel('alignmentPanel', 'alignmentOverlay', this.alignmentCloseDelayMs);

        // Only stop alignment (no toast) when panel is closed via X or overlay
        // Cancel button handles its own toast via the cancelled event
        if (!this.alignmentExplicitlyStopped) {
            this.setAlignmentBusy(true);
            this.alignmentManager.stop();
        }
        this.alignmentExplicitlyStopped = false;

        setTimeout(() => {
            this.alignmentPanelClosing = false;
        }, this.alignmentCloseDelayMs + 50);
    }

    updateDeviceHeaderStatus(status) {
        const container = this.$('deviceConnectionStatus');
        const icon = container?.querySelector('.material-icons');
        const text = this.$('deviceConnectionText');
        const hint = this.$('deviceConnectionHint');

        if (!container || !icon || !text || !hint) return;

        container.classList.remove('connected', 'connecting', 'disconnected', 'error');

        const isManualAction = this.pendingDeviceAction === 'manual-connect' || this.pendingDeviceAction === 'manual-disconnect';
        const stateCopy = {
            connected: {
                stateClass: 'connected',
                icon: 'bluetooth_connected',
                text: 'Connected',
                hint: status.deviceName ? `Ready • ${status.deviceName}` : 'Device ready'
            },
            scanning: {
                stateClass: 'connecting',
                icon: 'bluetooth_searching',
                text: 'Scanning',
                hint: isManualAction ? 'Searching...' : 'Auto-scanning'
            },
            connecting: {
                stateClass: 'connecting',
                icon: 'sync',
                text: 'Connecting',
                hint: isManualAction ? 'Opening connection...' : 'Auto-connecting'
            },
            error: {
                stateClass: 'error',
                icon: 'error',
                text: 'Error',
                hint: status.lastError || 'Connection failed'
            },
            disconnected: {
                stateClass: 'disconnected',
                icon: 'bluetooth_disabled',
                text: 'Disconnected',
                hint: 'Auto-connect active'
            }
        };
        const copy = stateCopy[status.connectionStatus] || stateCopy.disconnected;

        container.classList.add(copy.stateClass);
        icon.textContent = copy.icon;
        text.textContent = copy.text;
        hint.textContent = copy.hint;
    }

    setAlignmentBusy(isBusy) {
        this.alignmentInFlight = isBusy;

        ['saveAlignmentBtn', 'cancelAlignmentBtn', 'leftHandedBtn', 'rightHandedBtn', 'retryAlignmentBtn'].forEach(id => {
            const element = this.$(id);
            if (element) {
                element.disabled = isBusy;
            }
        });
    }

    setAlignmentError(message) {
        const errorElement = this.$('alignmentError');
        const textElement = errorElement?.querySelector('.error-text');

        if (!errorElement || !textElement) return;

        if (message) {
            textElement.textContent = message;
            this.setHidden(errorElement, false);
        } else {
            textElement.textContent = '';
            this.setHidden(errorElement, true);
        }
    }

    retryAlignment() {
        if (this.alignmentInFlight) return;
        this.setAlignmentError('');
        this.setAlignmentBusy(true);
        this.alignmentManager.start();
    }

    setFieldError(fieldId) {
        const field = this.$(fieldId);
        if (field) {
            field.classList.add('error');
        }
    }

    clearFieldError(fieldId) {
        const field = this.$(fieldId);
        if (field) {
            field.classList.remove('error');
        }
    }

    updateAlignmentDisplay(angle, isAligned) {
        const angleElement = this.$('alignmentAngle');
        const directionElement = this.$('alignmentDirection');
        const statusElement = this.$('alignedStatus');
        const pointerElement = this.$('aimPointer');

        if (!angleElement) return; // Not on alignment screen

        // Flip angle sign for left-handed users
        let displayAngle = angle;
        if (this.currentHandedness === 'left') {
            displayAngle = -angle;
        }

        // Format angle
        const formattedAngle = Math.abs(displayAngle).toFixed(1);
        angleElement.textContent = `${formattedAngle}°`;

        // Update direction text
        if (Math.abs(displayAngle) < 0.5) {
            directionElement.textContent = 'Aimed straight';
        } else if (displayAngle > 0) {
            directionElement.textContent = `Aimed ${formattedAngle}° right`;
        } else {
            directionElement.textContent = `Aimed ${formattedAngle}° left`;
        }

        // Update angle color based on magnitude
        angleElement.classList.remove('aligned', 'close', 'far');
        if (isAligned) {
            angleElement.classList.add('aligned');
        } else if (Math.abs(angle) < 5) {
            angleElement.classList.add('close');
        } else {
            angleElement.classList.add('far');
        }

        // Update status indicator
        statusElement.classList.remove('aligned', 'not-aligned');
        const iconElement = statusElement.querySelector('.aligned-icon');
        const textElement = statusElement.querySelector('.aligned-text');

        if (isAligned) {
            statusElement.classList.add('aligned');
            if (iconElement) iconElement.textContent = '✅';
            if (textElement) textElement.textContent = 'Aligned!';
        } else {
            statusElement.classList.add('not-aligned');
            if (iconElement) iconElement.textContent = '⚠️';
            if (textElement) textElement.textContent = 'Not aligned';
        }

        // Rotate compass pointer
        if (pointerElement) {
            pointerElement.setAttribute('transform', `rotate(${angle} 100 100)`);
        }
    }

    updateHandednessDisplay(handedness) {
        const leftBtn = this.$('leftHandedBtn');
        const rightBtn = this.$('rightHandedBtn');

        if (leftBtn && rightBtn) {
            if (handedness === 'left') {
                leftBtn.classList.add('active');
                rightBtn.classList.remove('active');
            } else {
                rightBtn.classList.add('active');
                leftBtn.classList.remove('active');
            }
        }
    }

    async loadFeatures() {
        try {
            const response = await this.api.get('/api/features');
            if (response.ok) {
                this.features = await response.json();
                this.applyFeatures();
            }
        } catch (error) {
            console.error('Failed to load features:', error);
        }
    }

    applyFeatures() {
        // Integration availability (camera included) is now driven by the
        // backend manifest list, so there is nothing feature-gated to toggle here.
    }

    applySettings(settings) {
        const spinMode = settings.spinMode || 'advanced';
        const spinModeRadio = document.querySelector(`input[name="spinMode"][value="${spinMode}"]`);
        if (spinModeRadio) spinModeRadio.checked = true;

        const omniSpeedUnit = this.$('omniSpeedUnit');
        const omniDistanceUnit = this.$('omniDistanceUnit');
        const omniGreenSpeed = this.$('omniGreenSpeed');
        const omniCarryAdjustment = this.$('omniCarryAdjustment');
        if (omniSpeedUnit) omniSpeedUnit.value = settings.omniSpeedUnit || 'mps';
        if (omniDistanceUnit) omniDistanceUnit.value = settings.omniDistanceUnit || 'meters';
        if (omniGreenSpeed) omniGreenSpeed.value = String(settings.omniGreenSpeed || 10);
        if (omniCarryAdjustment) omniCarryAdjustment.value = settings.omniCarryAdjustment ?? 0;
    }

    async saveSettings() {
        const spinMode = document.querySelector('input[name="spinMode"]:checked')?.value;
        const omniSpeedUnit = this.$('omniSpeedUnit')?.value || 'mps';
        const omniDistanceUnit = this.$('omniDistanceUnit')?.value || 'meters';
        const omniGreenSpeed = parseInt(this.$('omniGreenSpeed')?.value || '10', 10);
        const omniCarryAdjustment = parseInt(this.$('omniCarryAdjustment')?.value || '0', 10);
        await this.settingsManager.save({
            ...this.settingsManager.getAll(),
            spinMode,
            omniSpeedUnit,
            omniDistanceUnit,
            omniGreenSpeed,
            omniCarryAdjustment
        });
    }
}
