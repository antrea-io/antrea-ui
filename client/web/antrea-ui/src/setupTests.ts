/**
 * Copyright 2023 Antrea Authors.
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

// jest-dom adds custom jest matchers for asserting on DOM nodes.
// allows you to do things like:
// expect(element).toHaveTextContent(/react/i)
// learn more: https://github.com/testing-library/jest-dom
import '@testing-library/jest-dom/vitest';
import { mockIntersectionObserver,  mockResizeObserver } from 'jsdom-testing-mocks';

// jsdom doesn't implement these observers; some Lit components use them.
mockIntersectionObserver();

mockResizeObserver();

// Node 22+ ships an experimental global `localStorage` (https://nodejs.org/api/globals.html
// #localstorage) that silently returns undefined for every access unless the process is started
// with --localstorage-file. Vitest's jsdom environment sees that global already exists and skips
// installing jsdom's own (working) Storage over it, so without this, every localStorage access in
// a test run just returns undefined — not an error, which makes it look like a real code bug (see
// logout.tsx's `localStorage.removeItem(...)`, which throws on the undefined result). Replace it
// with a minimal in-memory Storage: nothing here needs the storage `event`.
class MemoryStorage implements Storage {
    [name: string]: unknown;
    private store = new Map<string, string>();
    get length() { return this.store.size; }
    clear() { this.store.clear(); }
    getItem(key: string) { return this.store.has(key) ? this.store.get(key)! : null; }
    key(index: number) { return Array.from(this.store.keys())[index] ?? null; }
    removeItem(key: string) { this.store.delete(key); }
    setItem(key: string, value: string) { this.store.set(key, String(value)); }
}
Object.defineProperty(globalThis, 'localStorage', { value: new MemoryStorage(), configurable: true });
