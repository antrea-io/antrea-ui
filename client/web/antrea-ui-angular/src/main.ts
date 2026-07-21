import '@cds/core/icon/register.js';
import {
  ClarityIcons,
  dashboardIcon,
  routerIcon,
  eyeIcon,
  cogIcon,
  logoutIcon,
  shieldCheckIcon,
  boltIcon,
} from '@cds/core/icon';
import '@antrea/ui-components/dist';

import { bootstrapApplication } from '@angular/platform-browser';
import { appConfig } from './app/app.config';
import { App } from './app/app';
import { setPluginAuthTokenAccessor } from './app/plugins';
import { AuthService } from './app/core/auth.service';

ClarityIcons.addIcons(dashboardIcon, routerIcon, eyeIcon, cogIcon, logoutIcon, shieldCheckIcon, boltIcon);

bootstrapApplication(App, appConfig)
  .then((appRef) => {
    const auth = appRef.injector.get(AuthService);
    setPluginAuthTokenAccessor(() => auth.token());
  })
  .catch((err) => console.error(err));
