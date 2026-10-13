import { Link } from "react-router";
import { PageHeader } from "@/components/PageHeader";

export function NotFound() {
  return (
    <PageHeader
      title="Page not found"
      description={
        <>
          This page does not exist or you do not have access to it.{" "}
          <Link to="/" className="text-brand-600 underline">
            Go home
          </Link>
        </>
      }
    />
  );
}
