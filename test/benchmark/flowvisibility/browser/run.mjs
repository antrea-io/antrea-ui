// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Drives a headless Chromium on an antrea-ui flow visibility page and reports what the page costs
// the browser, as one JSON object per line on stdout. Started by flowbench (see ../README.md),
// which passes its configuration in FLOWBENCH_CONFIG and samples the browser's processes itself.

import { chromium } from 'playwright';

const cfg = JSON.parse(process.env.FLOWBENCH_CONFIG);
const emit = (event, data = {}) => process.stdout.write(JSON.stringify({ event, t: Date.now(), ...data }) + '\n');
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// Runs in the page before any of its own scripts. Only observes: it never touches the app.
function instrument() {
    const s = {
        longTasks: [],
        frames: 0,
        maxFrameGap: 0,
        lastFrame: 0,
        maxLoopLag: 0,
        page: null,
    };
    window.__flowbench = s;
    try {
        new PerformanceObserver((list) => {
            for (const e of list.getEntries()) s.longTasks.push(e.duration);
        }).observe({ type: 'longtask', buffered: true });
    } catch { /* not supported */ }
    const onFrame = (now) => {
        if (s.lastFrame) s.maxFrameGap = Math.max(s.maxFrameGap, now - s.lastFrame);
        s.lastFrame = now;
        s.frames++;
        requestAnimationFrame(onFrame);
    };
    requestAnimationFrame(onFrame);
    // Event loop lag: how late a 50ms timer fires.
    let expected = performance.now() + 50;
    setInterval(() => {
        const now = performance.now();
        s.maxLoopLag = Math.max(s.maxLoopLag, now - expected);
        expected = now + 50;
    }, 50);

    const findDeep = (root, selector) => {
        const found = root.querySelector(selector);
        if (found) return found;
        for (const el of root.querySelectorAll('*')) {
            if (el.shadowRoot) {
                const inner = findDeep(el.shadowRoot, selector);
                if (inner) return inner;
            }
        }
        return null;
    };
    // Reads what is on screen, and resets the counters. The flow store's size is read from the
    // page element's fields: private in TypeScript only, and read here, never written.
    s.collect = () => {
        if (!s.page || !s.page.isConnected) s.page = findDeep(document, 'antrea-flow-visibility-page');
        const p = s.page;
        const svg = p?.shadowRoot?.querySelector('#graph-svg');
        const out = {
            longTaskCount: s.longTasks.length,
            longTaskTotalMs: s.longTasks.reduce((a, b) => a + b, 0),
            longTaskMaxMs: s.longTasks.reduce((a, b) => Math.max(a, b), 0),
            frames: s.frames,
            maxFrameGapMs: s.maxFrameGap,
            maxLoopLagMs: s.maxLoopLag,
            mapNodes: svg ? svg.querySelectorAll('g[style*="grab"]').length : 0,
            mapEdges: svg ? svg.querySelectorAll('path[fill="none"]').length : 0,
            svgElements: svg ? svg.getElementsByTagName('*').length : 0,
            pageElements: p?.shadowRoot ? p.shadowRoot.querySelectorAll('*').length : 0,
            storeEntries: typeof p?._store?.size === 'function' ? p._store.size() : null,
            storeEvicted: typeof p?._store?.hasEvicted === 'function' ? p._store.hasEvicted() : null,
        };
        s.longTasks = [];
        s.frames = 0;
        s.maxFrameGap = 0;
        s.maxLoopLag = 0;
        return out;
    };
}

const CDP_METRICS = [
    'TaskDuration', 'ScriptDuration', 'LayoutDuration', 'RecalcStyleDuration',
    'JSHeapUsedSize', 'JSHeapTotalSize', 'Nodes', 'LayoutCount', 'RecalcStyleCount', 'JSEventListeners',
];

async function cdpMetrics(cdp) {
    const { metrics } = await cdp.send('Performance.getMetrics');
    const m = {};
    for (const { name, value } of metrics) if (CDP_METRICS.includes(name)) m[name] = value;
    return m;
}

async function main() {
    const browser = await chromium.launch({ headless: cfg.headless !== false });
    const context = await browser.newContext({ viewport: { width: cfg.width ?? 1600, height: cfg.height ?? 1000 } });
    await context.addInitScript(instrument);
    const page = await context.newPage();
    page.on('pageerror', (err) => emit('page-error', { message: String(err) }));

    await page.goto(cfg.url + '/', { waitUntil: 'domcontentloaded' });
    const status = await page.evaluate(async (token) => {
        const r = await fetch('/auth/login/token', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ token }),
        });
        return r.status;
    }, cfg.token);
    if (status !== 200) throw new Error(`token login failed with HTTP ${status}`);
    // Only from here: before the login, the page's session check failing with a 401 is expected.
    page.on('console', (msg) => {
        if (msg.type() === 'error') emit('console-error', { message: msg.text() });
    });

    const start = Date.now();
    await page.goto(cfg.url + cfg.path, { waitUntil: 'domcontentloaded' });
    const cdp = await context.newCDPSession(page);
    await cdp.send('Performance.enable', { timeDomain: 'timeTicks' });
    // The map's first node, or for the list view the first stored flow.
    await page.waitForFunction(() => {
        const c = window.__flowbench.collect();
        return c.mapNodes > 0 || (c.storeEntries > 0 && !c.svgElements);
    }, null, { timeout: (cfg.firstRenderTimeoutSeconds ?? 120) * 1000, polling: 100 });
    const firstRender = Date.now();
    emit('first-render', { ms: firstRender - start });

    // Samples are taken from the first render on. Those of the warmup are reported too, marked as
    // such, and tell how long the page took to settle: the first sample in which the main thread
    // was busy less than settleBusy of the time.
    await page.evaluate(() => window.__flowbench.collect());
    let prev = await cdpMetrics(cdp);
    let prevT = Date.now();
    const sample = async (phase) => {
        await sleep((cfg.sampleIntervalSeconds ?? 2) * 1000);
        const now = Date.now();
        const cur = await cdpMetrics(cdp);
        const pageStats = await page.evaluate(() => window.__flowbench.collect());
        const dt = (now - prevT) / 1000;
        const rate = (k) => (cur[k] - prev[k]) / dt;
        const s = {
            phase,
            dt,
            // Fraction of wall time the renderer's main thread spent running tasks.
            mainThreadBusy: rate('TaskDuration'),
            scriptBusy: rate('ScriptDuration'),
            layoutBusy: rate('LayoutDuration'),
            styleBusy: rate('RecalcStyleDuration'),
            layoutsPerSec: rate('LayoutCount'),
            styleRecalcsPerSec: rate('RecalcStyleCount'),
            jsHeapUsed: cur.JSHeapUsedSize,
            jsHeapTotal: cur.JSHeapTotalSize,
            domNodes: cur.Nodes,
            jsEventListeners: cur.JSEventListeners,
            fps: pageStats.frames / dt,
            ...pageStats,
        };
        emit('sample', s);
        prev = cur;
        prevT = now;
        return s;
    };

    const settleBusy = cfg.settleBusy ?? 0.2;
    let settled = false;
    const warmupEnd = firstRender + (cfg.warmupSeconds ?? 30) * 1000;
    while (Date.now() < warmupEnd) {
        const s = await sample('warmup');
        if (!settled && s.mainThreadBusy < settleBusy) {
            settled = true;
            emit('settled', { ms: Date.now() - firstRender });
        }
    }
    emit('measure-start', { settled });

    let tracing = false;
    if (cfg.traceFile) {
        await browser.startTracing(page, { path: cfg.traceFile, screenshots: false });
        tracing = true;
    }
    const end = Date.now() + (cfg.durationSeconds ?? 120) * 1000;
    const traceEnd = Date.now() + (cfg.traceSeconds ?? 20) * 1000;
    while (Date.now() < end) {
        await sample('measure');
        if (tracing && Date.now() >= traceEnd) {
            await browser.stopTracing();
            tracing = false;
            emit('trace-written', { path: cfg.traceFile });
        }
    }
    // Writing the trace and closing the browser are not part of the measurement.
    emit('done');
    if (tracing) {
        await browser.stopTracing();
        emit('trace-written', { path: cfg.traceFile });
    }
    await browser.close();
}

main().catch((err) => {
    emit('fatal', { message: String(err?.stack ?? err) });
    process.exit(1);
});
