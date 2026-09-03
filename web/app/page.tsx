// Read-only storefront backed by the seeded catalog. Everything shown here is a
// product row, including what each item pairs with and on what terms.
import { loadPublicProducts } from "@/lib/catalog";
import { money } from "@/lib/money";
import { productArt } from "@/lib/product-art";
import { createClient } from "@/lib/supabase/server";
import { TopNav } from "./ui";

export default async function HomePage() {
  const [products, supabase] = [
    await loadPublicProducts(),
    await createClient(),
  ];
  const {
    data: { user },
  } = await supabase.auth.getUser();
  const buyHref = user ? "/dashboard#telegram" : "/login";
  const buyLabel = user ? "Buy in Telegram" : "Sign in to buy";
  const nameById = new Map(
    products.map((product) => [product.id, product.name]),
  );
  return (
    <main className="min-h-screen bg-paper px-6 py-10">
      <div className="mx-auto max-w-7xl">
        <TopNav
          current="store"
          action={
            <a
              className="bg-moss px-3 py-2 font-semibold text-paper"
              href={user ? "/dashboard" : "/login"}
            >
              {user ? "Your account" : "Sign in"}
            </a>
          }
        />

        <section id="catalog" className="mt-8 min-w-0">
          <div className="flex flex-wrap items-end justify-between gap-4 border-b border-ink/10 pb-5">
            <div>
              <p className="text-sm font-medium text-moss">The shelf</p>
              <h2 className="mt-1 text-3xl font-semibold tracking-tight">
                Everything in stock
              </h2>
            </div>
            <div className="text-right text-sm text-ink/70">
              <p>{products.length} products on the shelf</p>
              <p className="mt-1">
                Price, stock and pairing exactly as the shop quotes them
              </p>
            </div>
          </div>

          {products.length === 0 ? (
            <div className="mt-8 border border-dashed border-ink/20 bg-white p-8 text-sm text-ink/70">
              The shelf could not be read just now. Nothing has been lost, so
              try again in a moment.
            </div>
          ) : (
            <div className="mt-6 grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
              {products.map((product) => {
                const partner = product.combo_with
                  ? nameById.get(product.combo_with)
                  : undefined;
                const art = productArt(product);
                return (
                  <article
                    key={product.id}
                    className="flex flex-col border border-ink/10 bg-white shadow-sm"
                  >
                    {product.image_url ? (
                      /* eslint-disable-next-line @next/next/no-img-element */
                      <img
                        src={product.image_url}
                        alt={product.name}
                        className="h-40 w-full object-cover"
                      />
                    ) : (
                      <div
                        aria-hidden="true"
                        className="flex h-40 w-full items-center justify-center"
                        style={{
                          backgroundColor: art.background,
                          color: art.foreground,
                        }}
                      >
                        <span className="text-4xl font-semibold tracking-[0.12em]">
                          {art.monogram}
                        </span>
                      </div>
                    )}
                    <div className="flex flex-1 flex-col p-5">
                      <div className="flex items-start justify-between gap-4">
                        <div>
                          <p className="text-xs font-semibold uppercase tracking-[0.14em] text-moss">
                            {product.category}
                          </p>
                          <h3 className="mt-2 text-lg font-semibold">
                            {product.name}
                          </h3>
                          <p className="mt-1 text-xs text-ink/70">
                            {product.warranty_years} year warranty
                          </p>
                        </div>
                        <span className="bg-mint px-2 py-1 text-xs font-semibold text-moss">
                          Trust {product.trust_score}
                        </span>
                      </div>

                      {partner ? (
                        <p className="mt-4 bg-paper px-3 py-2 text-xs text-ink/80">
                          Pairs with {partner}
                          {product.combo_discount_pct
                            ? `, ${product.combo_discount_pct}% off the pair`
                            : ""}
                        </p>
                      ) : null}

                      <div className="mt-6 flex items-end justify-between border-t border-ink/10 pt-4">
                        <div>
                          <p className="text-xl font-semibold">
                            {money(product.price_paise)}
                          </p>
                          <a
                            href={buyHref}
                            className="mt-3 inline-block text-sm font-semibold text-moss hover:underline"
                          >
                            {buyLabel}
                          </a>
                        </div>
                        <div className="text-right">
                          <code
                            className="text-xs text-ink/70"
                            title="Send this with /buy in the chat"
                          >
                            {product.id.slice(0, 8)}
                          </code>
                          <p
                            className={
                              product.stock <= 3
                                ? "text-sm font-semibold text-coral"
                                : "text-sm text-ink/70"
                            }
                          >
                            {product.stock} in stock
                          </p>
                        </div>
                      </div>
                    </div>
                  </article>
                );
              })}
            </div>
          )}
        </section>
      </div>
    </main>
  );
}
