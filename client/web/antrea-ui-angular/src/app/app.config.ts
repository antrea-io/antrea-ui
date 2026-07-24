import { ApplicationConfig, provideBrowserGlobalErrorListeners } from '@angular/core';
import { provideRouter } from '@angular/router';
import { provideAnimationsAsync } from '@angular/platform-browser/animations/async';
import { ANTREA_PLUGIN_HOST } from '@antrea/ui-plugin-sdk';

import { routes } from './app.routes';
import { pluginHost } from './plugins';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    provideRouter(routes),
    provideAnimationsAsync(),
    { provide: ANTREA_PLUGIN_HOST, useValue: pluginHost },
  ]
};
