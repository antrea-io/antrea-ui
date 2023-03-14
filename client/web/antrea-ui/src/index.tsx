import React from 'react';
import ReactDOM from 'react-dom/client';
import './index.css';
import App from './App';
import Summary from './routes/summary';
import Traceflow from './routes/traceflow';
import TraceflowResult from './routes/traceflowresult';
import Settings from './routes/settings';
import reportWebVitals from './reportWebVitals';
import {
  createBrowserRouter,
  RouterProvider,
} from "react-router-dom";

const router = createBrowserRouter([
    {
        path: "/",
        element: <App />,
        children: [
            {
                // default route
                index: true,
                element: <Summary />,
            },
            {
                path: "summary",
                element: <Summary />,
            },
            {
                path: "traceflow",
                element: <Traceflow />,
                children: [
                    {
                        path: "result",
                        element: <TraceflowResult />,
                    }
                ]
            },
            {
                path: "settings",
                element: <Settings />,
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
