import { useLocation } from "react-router";
import { buttonVariants } from "@/components/ui/button-variants";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Logo } from "@/components/Logo";

/** Shown when there is no session. The login itself happens at the IdP. */
export function SignIn() {
  const { pathname, search } = useLocation();
  const returnTo = encodeURIComponent(pathname + search);
  return (
    <Centered>
      <CardHeader>
        <CardTitle>Sign in to Grantra</CardTitle>
        <CardDescription>
          You will sign in with your company account and come straight back here.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <a href={`/auth/login?return_to=${returnTo}`} className={buttonVariants({ size: "lg" })}>
          Sign in
        </a>
      </CardContent>
    </Centered>
  );
}

export function LoginNotConfigured() {
  return (
    <Centered>
      <CardHeader>
        <CardTitle>Console login is not configured</CardTitle>
        <CardDescription>
          Grantra is running, but no login provider is set. An administrator needs to set{" "}
          <code>GRANTRA_OIDC_ISSUER</code> and the related settings.
        </CardDescription>
      </CardHeader>
    </Centered>
  );
}

export function Centered({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-6 p-5">
      <Logo />
      <Card className="w-full max-w-sm">{children}</Card>
    </div>
  );
}
