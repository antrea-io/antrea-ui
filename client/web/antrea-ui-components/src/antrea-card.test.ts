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
import { afterEach, describe, expect, test, vi } from 'vitest';
import './antrea-card';
import type { AntreaCard } from './antrea-card';

afterEach(() => {
    vi.restoreAllMocks();
    document.body.replaceChildren();
});

// jsdom has no layout, so give the header a width that depends on its font size: a heading
// "needs" 12px of width per px of font size, and the card offers 120px.
function stubLayout(needPerPx: number, available = 120) {
    vi.spyOn(window, 'getComputedStyle').mockImplementation((el: Element) => (
        { fontSize: (el as HTMLElement).style.fontSize || '14px' } as CSSStyleDeclaration
    ));
    const px = (el: HTMLElement) => parseFloat(el.style.fontSize || '14');
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(available);
    vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockImplementation(function (this: HTMLElement) {
        return this.classList.contains('break') ? available : needPerPx * px(this);
    });
}

async function mount(heading: string): Promise<HTMLElement> {
    const card = document.createElement('antrea-card') as AntreaCard;
    card.heading = heading;
    document.body.append(card);
    await card.updateComplete;
    return card.shadowRoot!.querySelector('.card-header') as HTMLElement;
}

describe('antrea-card heading fit', () => {
    test('leaves a heading that fits at the themed size', async () => {
        stubLayout(5);
        const header = await mount('Pods');
        expect(header.style.fontSize).toBe('');
        expect(header.classList.contains('break')).toBe(false);
    });

    test('shrinks a heading that is too wide until it fits', async () => {
        stubLayout(10);
        const header = await mount('Antrea Cluster Network Policies');
        expect(parseFloat(header.style.fontSize)).toBe(12);
        expect(header.classList.contains('break')).toBe(false);
    });

    test('breaks inside the word once it is as small as it may get', async () => {
        stubLayout(100);
        const header = await mount('Supercalifragilistic');
        expect(parseFloat(header.style.fontSize)).toBe(10);
        expect(header.classList.contains('break')).toBe(true);
    });
});

describe('antrea-card resize observation', () => {
    const original = globalThis.ResizeObserver;
    afterEach(() => { globalThis.ResizeObserver = original; });

    test('observes its card again after being moved in the DOM', async () => {
        const observed: Element[] = [];
        let live = 0;
        globalThis.ResizeObserver = class {
            observe(el: Element) { observed.push(el); live += 1; }
            unobserve() {}
            disconnect() { live = 0; }
        };
        const card = document.createElement('antrea-card') as AntreaCard;
        document.body.append(card);
        await card.updateComplete;
        expect(live).toBe(1);

        // Disconnecting stops the old observer, and connecting builds a new one, which has to be
        // given the card again: firstUpdated does not run a second time.
        const parent = document.createElement('div');
        document.body.append(parent);
        parent.append(card);
        await card.updateComplete;
        expect(live).toBe(1);
        expect(observed).toHaveLength(2);
        expect(observed[1]).toBe(card.shadowRoot!.querySelector('.card'));
    });
});
