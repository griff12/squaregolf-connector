// features/IntegrationsManager.js
//
// Renders the integrations screen entirely from the backend's manifest list
// (GET /api/integrations). The UI has no hardcoded knowledge of GSPro, the
// camera, or any specific integration — adding a plugin makes a card appear with
// no frontend changes.
export class IntegrationsManager {
    constructor(api, toast) {
        this.api = api;
        this.toast = toast;
        this.container = document.getElementById('integrationsContainer');
        this.cards = new Map(); // name -> card element
    }

    async init() {
        try {
            const res = await this.api.get('/api/integrations');
            const integrations = await res.json();
            this.render(integrations);
        } catch (err) {
            console.error('Failed to load integrations:', err);
        }
    }

    render(integrations) {
        if (!this.container) return;
        this.container.innerHTML = '';
        this.cards.clear();
        for (const integration of integrations) {
            const card = this.buildCard(integration);
            this.container.appendChild(card);
            this.cards.set(integration.name, card);
        }
    }

    // updateStatus handles an integrationStatus WebSocket message.
    updateStatus(view) {
        const card = this.cards.get(view.name);
        if (!card) return;
        this.applyStatus(card, view);
    }

    buildCard(it) {
        const card = document.createElement('div');
        card.className = 'card';
        card.dataset.integration = it.name;

        const header = document.createElement('div');
        header.className = 'card-header';
        header.innerHTML = `<h3><span class="material-icons">${it.icon || 'extension'}</span> ${it.displayName}</h3>`;
        card.appendChild(header);

        const content = document.createElement('div');
        content.className = 'card-content';

        const status = document.createElement('div');
        status.className = 'status-value';
        status.dataset.role = 'status';
        content.appendChild(status);

        const error = document.createElement('div');
        error.className = 'error-message hidden';
        error.dataset.role = 'error';
        content.appendChild(error);

        // Config form generated from the manifest schema.
        const fields = it.configSchema || [];
        for (const field of fields) {
            content.appendChild(this.buildField(field, (it.config || {})[field.key]));
        }

        const buttons = document.createElement('div');
        buttons.className = 'button-group';

        if (fields.length) {
            const saveBtn = document.createElement('button');
            saveBtn.className = 'btn btn-primary';
            saveBtn.textContent = 'Save';
            saveBtn.addEventListener('click', () => this.save(it.name, fields));
            buttons.appendChild(saveBtn);
        }

        if (it.connectable) {
            const connectBtn = document.createElement('button');
            connectBtn.className = 'btn btn-primary';
            connectBtn.textContent = 'Connect';
            connectBtn.dataset.role = 'connect';
            connectBtn.addEventListener('click', () => this.connect(it.name));
            buttons.appendChild(connectBtn);

            const disconnectBtn = document.createElement('button');
            disconnectBtn.className = 'btn btn-secondary';
            disconnectBtn.textContent = 'Disconnect';
            disconnectBtn.dataset.role = 'disconnect';
            disconnectBtn.addEventListener('click', () => this.disconnect(it.name));
            buttons.appendChild(disconnectBtn);
        }

        for (const action of it.actions || []) {
            const actionBtn = document.createElement('button');
            actionBtn.className = action.style === 'primary' ? 'btn btn-primary' : 'btn btn-secondary';
            actionBtn.textContent = action.label;
            if (action.description) actionBtn.title = action.description;
            actionBtn.addEventListener('click', () => this.invokeAction(it.name, action));
            buttons.appendChild(actionBtn);
        }

        content.appendChild(buttons);
        card.appendChild(content);

        this.applyStatus(card, it);
        return card;
    }

    buildField(field, value) {
        const group = document.createElement('div');
        group.className = 'form-group';

        if (field.type === 'toggle') {
            const label = document.createElement('label');
            label.className = 'checkbox-label';
            const input = document.createElement('input');
            input.type = 'checkbox';
            input.dataset.key = field.key;
            input.checked = Boolean(value);
            label.appendChild(input);
            label.appendChild(document.createTextNode(' ' + field.label));
            group.appendChild(label);
        } else {
            const label = document.createElement('label');
            label.textContent = field.label + ':';
            const input = document.createElement('input');
            input.type = field.type === 'number' ? 'number' : 'text';
            input.className = 'input-field';
            input.dataset.key = field.key;
            if (value !== undefined && value !== null) input.value = value;
            group.appendChild(label);
            group.appendChild(input);
        }

        if (field.help) {
            const help = document.createElement('p');
            help.className = 'helper-text';
            help.textContent = field.help;
            group.appendChild(help);
        }
        return group;
    }

    collect(name, fields) {
        const card = this.cards.get(name);
        const values = {};
        for (const field of fields) {
            const input = card.querySelector(`[data-key="${field.key}"]`);
            if (!input) continue;
            if (field.type === 'toggle') {
                values[field.key] = input.checked;
            } else if (field.type === 'number') {
                values[field.key] = Number(input.value);
            } else {
                values[field.key] = input.value;
            }
        }
        return values;
    }

    async save(name, fields) {
        try {
            const res = await this.api.post(`/api/integrations/${name}/config`, this.collect(name, fields));
            if (!res.ok) throw new Error(await res.text());
            this.toast.success('Settings saved');
        } catch (err) {
            this.toast.error(`Failed to save: ${err.message}`);
        }
    }

    async connect(name) {
        try {
            const res = await this.api.post(`/api/integrations/${name}/connect`);
            if (!res.ok) throw new Error(await res.text());
            this.toast.info('Connecting...');
        } catch (err) {
            this.toast.error(`Connect failed: ${err.message}`);
        }
    }

    async disconnect(name) {
        try {
            const res = await this.api.post(`/api/integrations/${name}/disconnect`);
            if (!res.ok) throw new Error(await res.text());
            this.toast.info('Disconnecting...');
        } catch (err) {
            this.toast.error(`Disconnect failed: ${err.message}`);
        }
    }

    async invokeAction(name, action) {
        try {
            const res = await this.api.post(`/api/integrations/${name}/actions/${action.key}`, {});
            if (!res.ok) throw new Error(await res.text());
            this.toast.success(`${action.label} completed`);
        } catch (err) {
            this.toast.error(`${action.label} failed: ${err.message}`);
        }
    }

    applyStatus(card, view) {
        const status = view.status || 'disconnected';
        const statusEl = card.querySelector('[data-role="status"]');
        if (statusEl) {
            statusEl.className = `status-value ${status}`;
            statusEl.textContent = status.charAt(0).toUpperCase() + status.slice(1);
        }
        const errorEl = card.querySelector('[data-role="error"]');
        if (errorEl) {
            if (view.error) {
                errorEl.textContent = view.error;
                errorEl.classList.remove('hidden');
            } else {
                errorEl.classList.add('hidden');
            }
        }
        const connectBtn = card.querySelector('[data-role="connect"]');
        const disconnectBtn = card.querySelector('[data-role="disconnect"]');
        if (connectBtn && disconnectBtn) {
            const connected = status === 'connected' || status === 'connecting';
            connectBtn.disabled = connected;
            disconnectBtn.disabled = !connected;
        }
    }
}
