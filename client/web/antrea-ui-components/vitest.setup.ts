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

// jsdom v29 + vitest v4: the jsdom Storage implementation is not accessible via
// the bare `localStorage` global inside vitest's vm sandbox. Provide a simple
// in-memory replacement so tests that use localStorage work correctly.
const localStorageMock = (() => {
    let store: Record<string, string> = {};
    return {
        getItem: (key: string) => store[key] ?? null,
        setItem: (key: string, value: string) => { store[key] = String(value); },
        removeItem: (key: string) => { delete store[key]; },
        clear: () => { store = {}; },
        get length() { return Object.keys(store).length; },
        key: (index: number) => Object.keys(store)[index] ?? null,
    };
})();
Object.defineProperty(globalThis, 'localStorage', {
    value: localStorageMock,
    writable: true,
});
