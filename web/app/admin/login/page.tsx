// The operator door. Separate from the customer form on purpose: a refused
// attempt says so here rather than dropping someone onto the storefront login,
// and this page grants nothing by itself.
import { signInAsOperator, signOutToOperatorDoor } from "@/app/auth/actions";
import { currentIdentity } from "@/lib/roles";
import Link from "next/link";
import { redirect } from "next/navigation";

export default async function AdminLoginPage({
  searchParams,
}: {
  searchParams: Promise<{ error?: string }>;
}) {
  const params = await searchParams;
  const identity = await currentIdentity();
  if (identity?.role === "admin") redirect("/admin");

  return (
    <main className="flex min-h-screen items-center justify-center bg-paper px-6">
      <section className="w-full max-w-md border border-ink/10 bg-white p-8 shadow-sm">
        <p className="text-xs font-semibold uppercase tracking-[0.18em] text-moss">
          Operator access
        </p>
        <h1 className="mt-3 text-3xl font-semibold">Sign in to operations</h1>

        {identity ? (
          <>
            <p className="mt-2 text-sm text-ink/70">
              You are signed in as {identity.email}, which is a customer
              account. Operator access is granted per account in the database,
              not by who you are signed in as.
            </p>
            <p className="mt-4 border border-coral/30 bg-coral/10 px-3 py-2 text-sm text-coral">
              This account cannot read the operator view.
            </p>
            <form action={signOutToOperatorDoor} className="mt-8">
              <button
                className="w-full bg-ink px-4 py-3 text-sm font-semibold text-paper"
                type="submit"
              >
                Sign out and use another account
              </button>
            </form>
            <p className="mt-4 text-xs">
              <Link href="/dashboard" className="text-moss hover:underline">
                Back to your dashboard
              </Link>
            </p>
          </>
        ) : (
          <>
            <p className="mt-2 text-sm text-ink/60">
              For accounts marked as operators. Everyone else should use the
              customer sign in.
            </p>
            {params.error ? (
              <p className="mt-4 border border-coral/30 bg-coral/10 px-3 py-2 text-sm text-coral">
                {params.error}
              </p>
            ) : null}
            <form action={signInAsOperator} className="mt-8">
              <button
                className="w-full bg-ink px-4 py-3 text-sm font-semibold text-paper"
                type="submit"
              >
                Continue with Google
              </button>
            </form>
            <p className="mt-4 text-xs">
              <Link href="/login" className="text-moss hover:underline">
                Customer sign in
              </Link>
            </p>
          </>
        )}
      </section>
    </main>
  );
}
