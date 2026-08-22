// Login and account creation form.
import { signIn, signUp } from "@/app/auth/actions";

export default async function LoginPage({ searchParams }: { searchParams: Promise<{ error?: string; registered?: string }> }) {
  const params = await searchParams;
  return (
    <main className="flex min-h-screen items-center justify-center bg-paper px-6">
      <section className="w-full max-w-md border border-ink/10 bg-white p-8 shadow-sm">
        <p className="text-xs font-semibold uppercase tracking-[0.18em] text-moss">AgentMart</p>
        <h1 className="mt-3 text-3xl font-semibold">Sign in to operations</h1>
        <p className="mt-2 text-sm text-ink/60">Use your Supabase account to manage wallet commerce.</p>
        {params.error ? <p className="mt-4 border border-coral/30 bg-coral/10 px-3 py-2 text-sm text-coral">{params.error}</p> : null}
        {params.registered ? <p className="mt-4 border border-moss/30 bg-mint px-3 py-2 text-sm text-moss">Account created. Check your email if confirmation is enabled.</p> : null}
        <form action={signIn} className="mt-8 space-y-4">
          <label className="block text-sm font-medium">Email<input name="email" type="email" required className="mt-2 w-full border border-ink/20 px-3 py-2" /></label>
          <label className="block text-sm font-medium">Password<input name="password" type="password" required className="mt-2 w-full border border-ink/20 px-3 py-2" /></label>
          <div className="flex gap-3">
            <button className="flex-1 bg-ink px-4 py-3 text-sm font-semibold text-paper" type="submit">Sign in</button>
            <button className="flex-1 border border-ink/20 px-4 py-3 text-sm font-semibold" formAction={signUp} type="submit">Create account</button>
          </div>
        </form>
        <p className="mt-4 text-xs text-ink/50">Email confirmation follows the Supabase project settings.</p>
      </section>
    </main>
  );
}
