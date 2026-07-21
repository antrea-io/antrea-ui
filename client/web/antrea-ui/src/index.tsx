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

import React from 'react';
import ReactDOM from 'react-dom/client';
import { createBrowserRouter } from 'react-router';
import { RouterProvider } from 'react-router/dom';
import { setApiBase } from '@antrea/ui-components';
import './index.css';
import App from './App';
import { SummaryPage, TraceflowPage, FlowVisibilityPage, SettingsPage } from './pages';
import reportWebVitals from './reportWebVitals';
import config from './config';

// Lets antrea-ui-components' fetch calls (login, refresh, settings, data pages) reach the
// backend when it's on a different origin than this frontend — e.g. local dev, where
// VITE_API_SERVER points at a separately-running backend and there's no dev proxy. A no-op in
// the normal deployed case, where VITE_API_SERVER is unset and nginx serves both from one origin.
setApiBase(config.apiServer);

const router = createBrowserRouter([
    {
        path: "/",
        element: <App />,
        children: [
            {
                index: true,
                element: <SummaryPage />,
            },
            {
                path: "summary",
                element: <SummaryPage />,
            },
            {
                path: "traceflow",
                element: <TraceflowPage />,
            },
            {
                path: "flows",
                element: <FlowVisibilityPage />,
            },
            {
                path: "settings",
                element: <SettingsPage />,
            },
        ],
    },
]);

const root = ReactDOM.createRoot(
  document.getElementById('root') as HTMLElement
);
root.render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>
);

// If you want to start measuring performance in your app, pass a function
// to log results (for example: reportWebVitals(console.log))
// or send to an analytics endpoint. Learn more: https://bit.ly/CRA-vitals
reportWebVitals();
