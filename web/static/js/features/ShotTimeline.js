// features/ShotTimeline.js
// Renders canonical launch-monitor shots and contributions from every plugin.
export class ShotTimeline {
    constructor(api) {
        this.api = api;
        this.container = document.getElementById('shotTimeline');
        this.empty = document.getElementById('shotTimelineEmpty');
        this.shots = new Map();
        this.maxShots = 20;
    }

    async init() {
        try {
            const response = await this.api.get(`/api/shots?limit=${this.maxShots}`);
            if (!response.ok) throw new Error(await response.text());
            this.setHistory(await response.json());
        } catch (error) {
            console.error('Failed to load shot timeline:', error);
        }
    }

    setHistory(shots) {
        this.shots.clear();
        for (const shot of shots || []) this.shots.set(shot.id, shot);
        this.render();
    }

    applyEvent(event) {
        if (!event?.shot?.id) return;
        this.shots.set(event.shot.id, event.shot);
        const ordered = this.orderedShots();
        for (const shot of ordered.slice(this.maxShots)) this.shots.delete(shot.id);
        this.render();
    }

    orderedShots() {
        return [...this.shots.values()].sort((a, b) =>
            (b.sequence || 0) - (a.sequence || 0));
    }

    render() {
        if (!this.container) return;
        this.container.replaceChildren();
        const shots = this.orderedShots();
        this.empty?.classList.toggle('hidden', shots.length > 0);
        for (const shot of shots) this.container.appendChild(this.buildShot(shot));
    }

    buildShot(shot) {
        const article = document.createElement('article');
        article.className = 'timeline-shot';
        article.dataset.shotId = shot.id;

        const header = document.createElement('div');
        header.className = 'timeline-shot-header';
        const title = document.createElement('div');
        title.className = 'timeline-shot-title';
        title.textContent = `Shot ${shot.sequence}`;
        const context = document.createElement('span');
        context.className = 'timeline-shot-context';
        context.textContent = [shot.clubName, this.formatTime(shot.occurredAt)].filter(Boolean).join(' · ');
        header.append(title, context);
        article.appendChild(header);

        article.appendChild(this.buildLaunchMetrics(shot));

        const results = document.createElement('div');
        results.className = 'timeline-results';
        for (const result of shot.results || []) results.appendChild(this.buildResult(result));
        if (!(shot.results || []).length) {
            const waiting = document.createElement('div');
            waiting.className = 'timeline-waiting';
            waiting.textContent = 'Waiting for integration feedback';
            results.appendChild(waiting);
        }
        article.appendChild(results);
        return article;
    }

    buildLaunchMetrics(shot) {
        const group = document.createElement('div');
        group.className = 'timeline-launch-metrics';
        const ball = shot.ball || {};
        const club = shot.club || {};
        const metrics = [
            ['Ball speed', typeof ball.speed === 'number' ? ball.speed * 2.23694 : null, 'mph'],
            ['Launch', ball.launchAngle, '°'],
            ['Spin', ball.totalSpin, 'rpm'],
            ['Club speed', typeof club.clubSpeed === 'number' ? club.clubSpeed * 2.23694 : null, 'mph'],
        ];
        for (const [label, value, unit] of metrics) {
            if (typeof value !== 'number' || Number.isNaN(value)) continue;
            group.appendChild(this.metricElement(label, value, unit));
        }
        return group;
    }

    buildResult(result) {
        const card = document.createElement('section');
        card.className = 'timeline-result';

        const header = document.createElement('div');
        header.className = 'timeline-result-header';
        const plugin = document.createElement('strong');
        plugin.textContent = result.plugin;
        const kind = document.createElement('span');
        kind.textContent = result.kind;
        header.append(plugin, kind);
        card.appendChild(header);

        if (result.summary) {
            const summary = document.createElement('p');
            summary.className = 'timeline-result-summary';
            summary.textContent = result.summary;
            card.appendChild(summary);
        }

        if (result.metrics?.length) {
            const metrics = document.createElement('div');
            metrics.className = 'timeline-plugin-metrics';
            for (const metric of result.metrics) {
                metrics.appendChild(this.metricElement(metric.label, metric.value, metric.unit, metric.status));
            }
            card.appendChild(metrics);
        }

        for (const insight of result.insights || []) {
            const item = document.createElement('div');
            item.className = `timeline-insight severity-${insight.severity || 'info'}`;
            const title = document.createElement('strong');
            title.textContent = insight.title;
            const message = document.createElement('p');
            message.textContent = insight.message;
            item.append(title, message);
            if (insight.recommendation) {
                const recommendation = document.createElement('p');
                recommendation.className = 'timeline-recommendation';
                recommendation.textContent = insight.recommendation;
                item.appendChild(recommendation);
            }
            card.appendChild(item);
        }

        for (const series of result.series || []) card.appendChild(this.seriesElement(series));

        if (result.media?.length) {
            const gallery = document.createElement('div');
            gallery.className = 'timeline-media';
            for (const media of result.media) gallery.appendChild(this.mediaElement(media));
            card.appendChild(gallery);
        }

        if (result.links?.length) {
            const links = document.createElement('div');
            links.className = 'timeline-links';
            for (const link of result.links) {
                const url = this.safeUrl(link.url);
                if (!url) continue;
                const anchor = document.createElement('a');
                anchor.href = url;
                anchor.textContent = link.label;
                anchor.target = '_blank';
                anchor.rel = 'noopener noreferrer';
                links.appendChild(anchor);
            }
            card.appendChild(links);
        }
        return card;
    }

    metricElement(label, value, unit = '', status = '') {
        const item = document.createElement('div');
        item.className = `timeline-metric ${status ? `metric-${status}` : ''}`;
        const name = document.createElement('span');
        name.textContent = label;
        const number = document.createElement('strong');
        const decimals = unit === 'rpm' ? 0 : 1;
        number.textContent = `${Number(value).toFixed(decimals)}${unit ? ` ${unit}` : ''}`;
        item.append(name, number);
        return item;
    }

    seriesElement(series) {
        const figure = document.createElement('figure');
        figure.className = 'timeline-series';
        const caption = document.createElement('figcaption');
        caption.textContent = `${series.label}${series.unit ? ` (${series.unit})` : ''}`;
        figure.appendChild(caption);
        const points = (series.points || []).filter(Number.isFinite);
        if (points.length < 2) return figure;

        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('viewBox', '0 0 300 80');
        svg.setAttribute('role', 'img');
        const min = Math.min(...points);
        const max = Math.max(...points);
        const span = max - min || 1;
        const pathPoints = points.map((value, index) => {
            const x = index * 300 / (points.length - 1);
            const y = 72 - ((value - min) / span) * 64;
            return `${x.toFixed(1)},${y.toFixed(1)}`;
        }).join(' ');
        const polyline = document.createElementNS('http://www.w3.org/2000/svg', 'polyline');
        polyline.setAttribute('points', pathPoints);
        svg.appendChild(polyline);
        figure.appendChild(svg);
        return figure;
    }

    mediaElement(media) {
        const item = document.createElement('figure');
        item.className = 'timeline-media-item';
        let element;
        if (media.type === 'video') {
            element = document.createElement('video');
            element.controls = true;
            element.preload = 'metadata';
            const poster = this.safeUrl(media.thumbnailUrl);
            if (poster) element.poster = poster;
        } else {
            element = document.createElement('img');
            element.loading = 'lazy';
            element.alt = media.label || 'Plugin result';
        }
        const url = this.safeUrl(media.url);
        if (!url) return item;
        element.src = url;
        item.appendChild(element);
        if (media.label) {
            const caption = document.createElement('figcaption');
            caption.textContent = media.label;
            item.appendChild(caption);
        }
        return item;
    }

    formatTime(value) {
        const date = new Date(value);
        return Number.isNaN(date.getTime()) ? '' : date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    }

    safeUrl(value) {
        try {
            const url = new URL(value, window.location.origin);
            return ['http:', 'https:'].includes(url.protocol) ? url.href : null;
        } catch (_error) {
            return null;
        }
    }
}
