/**
 * Copyright 2026 Antrea Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { useRef, useEffect, useState, useMemo } from 'react';
import { CdsCard } from '@cds/react/card';
import { NodeLink, NodeLatencyModel, heatmapNodeOrder, linkKey } from './nodelatency-util';

// Matches the green→yellow→red gradient from the original ECharts config.
const C_FAST: [number, number, number] = [90, 167, 0];
const C_MID: [number, number, number] = [253, 185, 19];
const C_SLOW: [number, number, number] = [225, 34, 0];
const C_DOWN: [number, number, number] = [79, 85, 98];

function lerp(a: number, b: number, t: number): number {
    return Math.round(a + (b - a) * t);
}

function rttToRgb(rttMs: number, minRtt: number, maxRtt: number): [number, number, number] {
    const t = maxRtt > minRtt ? (rttMs - minRtt) / (maxRtt - minRtt) : 0;
    const [lo, hi, s] = t <= 0.5 ? [C_FAST, C_MID, t * 2] : [C_MID, C_SLOW, (t - 0.5) * 2];
    return [lerp(lo[0], hi[0], s), lerp(lo[1], hi[1], s), lerp(lo[2], hi[2], s)];
}

// Maximum canvas side in logical pixels. cellPx = floor(MAX_CANVAS_PX / n), so
// total pixel writes are always O(MAX_CANVAS_PX²) regardless of cluster size.
// For n > MAX_CANVAS_PX, cellPx clamps to 1 and the browser CSS-scales the canvas
// down to fit — structural patterns (full row/column down) remain visible.
const MAX_CANVAS_PX = 1000;

interface TooltipState {
    clientX: number;
    clientY: number;
    source: string;
    target: string;
    self: boolean;
    link?: NodeLink;
}

export default function NodeLatencyHeatmap(props: { model: NodeLatencyModel; restrictToProblem?: boolean }) {
    const { model, restrictToProblem } = props;
    const canvasRef = useRef<HTMLCanvasElement>(null);
    const [tooltip, setTooltip] = useState<TooltipState | null>(null);

    const nodes = useMemo(
        () => heatmapNodeOrder(model, restrictToProblem ?? false),
        [model, restrictToProblem],
    );
    const n = nodes.length;

    const { buf, canvasSize, cellPx } = useMemo(() => {
        if (n === 0) return { buf: null as Uint8ClampedArray | null, canvasSize: 0, cellPx: 1 };

        const cellPx = Math.max(1, Math.floor(MAX_CANVAS_PX / n));
        const canvasSize = n * cellPx;
        const buf = new Uint8ClampedArray(canvasSize * canvasSize * 4);

        let minRtt = Infinity, maxRtt = 0;
        for (let y = 0; y < n; y++) {
            for (let x = 0; x < n; x++) {
                if (x === y) continue;
                const link = model.linkByKey.get(linkKey(nodes[y], nodes[x]));
                if (link && !link.down && link.rttMs !== undefined) {
                    if (link.rttMs < minRtt) minRtt = link.rttMs;
                    if (link.rttMs > maxRtt) maxRtt = link.rttMs;
                }
            }
        }
        if (minRtt === Infinity) minRtt = 0;

        for (let y = 0; y < n; y++) {
            for (let x = 0; x < n; x++) {
                let r = 0, g = 0, b = 0, a = 0;
                if (x !== y) {
                    const link = model.linkByKey.get(linkKey(nodes[y], nodes[x]));
                    if (!link) {
                        [r, g, b] = [0x20, 0x22, 0x26]; a = 255;
                    } else if (link.down || link.rttMs === undefined) {
                        [r, g, b] = C_DOWN; a = 255;
                    } else {
                        [r, g, b] = rttToRgb(link.rttMs, minRtt, maxRtt); a = 255;
                    }
                }
                for (let py = y * cellPx; py < (y + 1) * cellPx; py++) {
                    for (let px = x * cellPx; px < (x + 1) * cellPx; px++) {
                        const i = 4 * (py * canvasSize + px);
                        buf[i] = r; buf[i + 1] = g; buf[i + 2] = b; buf[i + 3] = a;
                    }
                }
            }
        }
        return { buf, canvasSize, cellPx };
    }, [nodes, model, n]);

    useEffect(() => {
        const canvas = canvasRef.current;
        if (!canvas || !buf || canvasSize === 0) return;
        canvas.width = canvasSize;
        canvas.height = canvasSize;
        const ctx = canvas.getContext('2d');
        if (!ctx) return;
        const imageData = ctx.createImageData(canvasSize, canvasSize);
        imageData.data.set(buf);
        ctx.putImageData(imageData, 0, 0);
    }, [buf, canvasSize]);

    const handleMouseMove = (e: React.MouseEvent<HTMLCanvasElement>) => {
        const canvas = canvasRef.current;
        if (!canvas || n === 0) return;
        const rect = canvas.getBoundingClientRect();
        const xi = Math.floor(((e.clientX - rect.left) * canvasSize / rect.width) / cellPx);
        const yi = Math.floor(((e.clientY - rect.top) * canvasSize / rect.height) / cellPx);
        if (xi < 0 || xi >= n || yi < 0 || yi >= n) { setTooltip(null); return; }
        const source = nodes[yi], target = nodes[xi];
        setTooltip({
            clientX: e.clientX,
            clientY: e.clientY,
            source,
            target,
            self: xi === yi,
            link: xi === yi ? undefined : model.linkByKey.get(linkKey(source, target)),
        });
    };

    const truncated = restrictToProblem && nodes.length < model.nodes.length
        ? { shown: nodes.length, total: model.nodes.length } : null;

    return (
        <CdsCard>
            <div cds-layout="vertical gap:sm">
                {truncated && (
                    <p cds-text="secondary">
                        Showing problem nodes only ({truncated.shown} of {truncated.total}).
                    </p>
                )}
                {n === 0 ? (
                    <p cds-text="secondary">No active nodes found.</p>
                ) : (
                    <div>
                        <canvas
                            ref={canvasRef}
                            style={{ maxWidth: '100%', display: 'block', cursor: 'crosshair' }}
                            onMouseMove={handleMouseMove}
                            onMouseLeave={() => setTooltip(null)}
                        />
                        {tooltip && (
                            <div style={{
                                position: 'fixed',
                                left: tooltip.clientX + 14,
                                top: tooltip.clientY + 14,
                                background: 'rgba(20,20,20,0.9)',
                                color: '#fff',
                                padding: '4px 8px',
                                borderRadius: 4,
                                fontSize: 12,
                                lineHeight: 1.5,
                                pointerEvents: 'none',
                                zIndex: 9999,
                            }}>
                                <div>{tooltip.source} &rarr; {tooltip.target}</div>
                                {tooltip.self ? null
                                    : !tooltip.link ? <div style={{ opacity: 0.6 }}>No measurement</div>
                                    : tooltip.link.down ? <div style={{ color: '#ff7070' }}>Down</div>
                                    : <div>{(tooltip.link.rttMs ?? 0).toFixed(3)} ms</div>
                                }
                            </div>
                        )}
                    </div>
                )}
                <div style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: 11, opacity: 0.65 }}>
                    <span style={{ width: 12, height: 12, borderRadius: 2, background: '#5aa700', flexShrink: 0, display: 'inline-block' }} />
                    Fast
                    <div style={{ width: 60, height: 10, borderRadius: 2, background: 'linear-gradient(to right, #5aa700, #fdb913, #e12200)', flexShrink: 0 }} />
                    Slow
                    <span style={{ marginLeft: 4, width: 12, height: 12, borderRadius: 2, background: '#4f5562', flexShrink: 0, display: 'inline-block' }} />
                    Down
                </div>
            </div>
        </CdsCard>
    );
}
