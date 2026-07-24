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
  userIcon,
  angleIcon,
  sunIcon,
  moonIcon,
} from '@cds/core/icon';
import '@antrea/ui-components/dist';

import { bootstrapApplication } from '@angular/platform-browser';
import { appConfig } from './app/app.config';
import { App } from './app/app';
import { setPluginAuthTokenAccessor } from './app/plugins';
import { AuthService } from './app/core/auth.service';

// angleIcon: clr-vertical-nav-group's own expand/collapse chevron (cds-icon shape="angle"
// in its internal template) — not used directly in our own templates, so easy to miss.
// sunIcon/moonIcon: the header's light/dark theme toggle button.
ClarityIcons.addIcons(dashboardIcon, routerIcon, eyeIcon, cogIcon, logoutIcon, shieldCheckIcon, boltIcon, userIcon, angleIcon, sunIcon, moonIcon);

bootstrapApplication(App, appConfig)
  .then((appRef) => {
    const auth = appRef.injector.get(AuthService);
    setPluginAuthTokenAccessor(() => auth.token());
  })
  .catch((err) => console.error(err));
