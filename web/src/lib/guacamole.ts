// Apache Guacamole's JavaScript client, which draws a remote desktop from
// the gateway's tunnel and plays desktop recordings. Its ESM build exports
// the Guacamole namespace as its default, while its types describe the
// same namespace as named exports.

import type * as Types from "guacamole-common-js";
import * as mod from "guacamole-common-js";

export const Guacamole = (mod as unknown as { default: typeof Types }).default;
export type { Types as GuacamoleTypes };
