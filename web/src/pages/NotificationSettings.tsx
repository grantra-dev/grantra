import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Loaded } from "@/components/Loaded";
import { PageHeader } from "@/components/PageHeader";
import { Alert } from "@/components/ui/alert";
import { getNotificationSettings, setNotification, type Me } from "@/lib/api";

export function NotificationSettings({ me }: { me: Me }) {
  const queryClient = useQueryClient();
  const { data, error, isPending } = useQuery({
    queryKey: ["notifications"],
    queryFn: getNotificationSettings,
  });
  const toggle = useMutation({
    mutationFn: ({ kind, enabled }: { kind: string; enabled: boolean }) =>
      setNotification(kind, enabled),
    onSuccess: (out) => queryClient.setQueryData(["notifications"], out),
  });

  return (
    <>
      <PageHeader
        title="Email notifications"
        description={
          me.email ? (
            <>
              Choose which emails Grantra sends to <strong>{me.email}</strong>.
            </>
          ) : (
            "Choose which emails Grantra sends you."
          )
        }
      />
      <div className="flex flex-col gap-4">
        {data && !data.email_enabled && (
          <Alert>
            This Grantra does not send email yet. Your choices are saved and apply once an admin
            sets up email.
          </Alert>
        )}
        {data && !data.has_email && (
          <Alert>You have no email address in Grantra, so you will not get any email.</Alert>
        )}
        {toggle.error && (
          <Alert variant="error">Could not save this change: {toggle.error.message}</Alert>
        )}
        <Loaded loading={isPending} error={error}>
          <ul className="divide-y divide-slate-200">
            {data?.kinds.map((k) => (
              <li key={k.kind}>
                <label className="flex cursor-pointer items-start gap-3 p-4">
                  <input
                    type="checkbox"
                    className="mt-1 size-4"
                    checked={k.enabled}
                    disabled={toggle.isPending}
                    onChange={(e) => toggle.mutate({ kind: k.kind, enabled: e.target.checked })}
                  />
                  <span>
                    <span className="block text-sm font-medium">{k.title}</span>
                    <span className="block text-sm text-slate-500">{k.description}</span>
                  </span>
                </label>
              </li>
            ))}
          </ul>
        </Loaded>
      </div>
    </>
  );
}
