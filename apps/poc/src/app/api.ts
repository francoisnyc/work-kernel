// The public API, as one person: a Connect client through the poc
// container's /api proxy (same origin), sending their bearer token.
import { createClient, type Client, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { ItemService } from "@gen/kernel/v1/kernel_pb";

export type Api = Client<typeof ItemService>;

export function apiFor(token: string, baseUrl = "/api"): Api {
  const auth: Interceptor = (next) => (req) => {
    req.header.set("Authorization", `Bearer ${token}`);
    return next(req);
  };
  return createClient(ItemService, createConnectTransport({ baseUrl, interceptors: [auth] }));
}
