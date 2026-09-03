// Login and account creation form.
import { signInWithGoogle } from "@/app/auth/actions";
import Link from "next/link";

export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<{ error?: string }>;
}) {
  const params = await searchParams;
  return (
    <main className="flex min-h-screen items-center justify-center bg-paper px-6">
      <section className="w-full max-w-md border border-ink/10 bg-white p-8 shadow-sm">
        <p className="text-xs font-semibold uppercase tracking-[0.18em] text-moss">
          AgentMart
        </p>
        <h1 className="mt-3 text-3xl font-semibold">Sign in to AgentMart</h1>
        <p className="mt-2 text-sm text-ink/60">
          Google creates or restores your Supabase-backed wallet account.
        </p>
        {params.error ? (
          <p className="mt-4 border border-coral/30 bg-coral/10 px-3 py-2 text-sm text-coral">
            {params.error}
          </p>
        ) : null}
        <form action={signInWithGoogle} className="mt-8">
          <button
            className="w-full bg-ink px-4 py-3 text-sm font-semibold text-paper"
            type="submit"
          >
            Continue with Google
          </button>
        </form>
        <p className="mt-4 text-xs text-ink/70">
          Google OAuth must be enabled in Supabase Auth providers.
        </p>
        <p className="mt-2 text-xs">
          <Link href="/" className="text-moss hover:underline">
            Back to the store
          </Link>
        </p>
      </section>
    </main>
  );
}
