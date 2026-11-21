// DEV ONLY. The sign-in screen: the dev-token file's people, staff and
// customers apart. Compiled in only with VITE_DEV_USERS=1.
import type { ReactElement } from "react";
import type { DevUser } from "../model/devusers";

export function SignIn(_props: { users: DevUser[]; onSignIn: (name: string) => void }): ReactElement | null {
  return null;
}
