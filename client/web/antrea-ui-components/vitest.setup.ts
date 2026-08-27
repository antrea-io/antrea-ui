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

// jsdom doesn't implement ResizeObserver; antrea-flow-visibility-page's service map uses one
// to size the SVG. Tests don't need it to actually report size changes, just to not throw.
class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
}
Object.defineProperty(globalThis, 'ResizeObserver', {
    value: ResizeObserverStub,
    writable: true,
});

// jsdom doesn't implement HTMLFormElement.requestSubmit() ("Not implemented" thrown at
// runtime) — antrea-button and antrea-input both call it to submit their owning form from
// inside a shadow root, where native implicit submission doesn't reach. Without this, tests
// can only dispatch a raw `submit` Event directly (bypassing the code under test entirely)
// instead of exercising the real click/Enter -> requestSubmit() path.
HTMLFormElement.prototype.requestSubmit = function (submitter?: HTMLElement) {
    if (submitter && !this.contains(submitter)) {
        throw new DOMException('The specified element is not owned by this form element', 'NotFoundError');
    }
    const event = new Event('submit', { bubbles: true, cancelable: true });
    if (submitter) Object.defineProperty(event, 'submitter', { value: submitter });
    this.dispatchEvent(event);
};

// Node 22+ ships an experimental global `localStorage` (https://nodejs.org/api/globals.html
// #localstorage) that silently returns undefined for every access unless the process is started
// with --localstorage-file. Vitest's jsdom environment sees that global already exists and skips
// installing jsdom's own (working) Storage over it, so without this, every localStorage access in
// a test run just returns undefined — not an error, which makes it look like a real code bug.
// Replace it with a minimal in-memory Storage: nothing here needs the storage `event`.
class MemoryStorage implements Storage {
    [name: string]: any;
    private store = new Map<string, string>();
    get length() { return this.store.size; }
    clear() { this.store.clear(); }
    getItem(key: string) { return this.store.has(key) ? this.store.get(key)! : null; }
    key(index: number) { return Array.from(this.store.keys())[index] ?? null; }
    removeItem(key: string) { this.store.delete(key); }
    setItem(key: string, value: string) { this.store.set(key, String(value)); }
}
Object.defineProperty(globalThis, 'localStorage', { value: new MemoryStorage(), configurable: true });

// jsdom's attachInternals() returns an ElementInternals whose `.form` getter always returns
// undefined — antrea-button/antrea-input rely on `.form` to find their owning form from
// inside a shadow root. Patch it to fall back to a light-DOM ancestor lookup, which covers
// the common case (no form="idref" attribute) that every current usage relies on.
const originalAttachInternals = HTMLElement.prototype.attachInternals;
HTMLElement.prototype.attachInternals = function (this: HTMLElement) {
    const internals = originalAttachInternals.call(this);
    Object.defineProperty(internals, 'form', {
        get: () => this.closest('form'),
    });
    return internals;
};
