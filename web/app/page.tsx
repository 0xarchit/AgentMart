// Read-only dashboard shell backed by the seeded catalog.
type Product = {
  id: string;
  name: string;
  category: string;
  price_paise: number;
  stock: number;
  trust_score: number;
};

async function loadProducts(): Promise<Product[]> {
  const baseURL = process.env.SUPABASE_URL;
  const publishableKey = process.env.SUPABASE_PUBLISHABLE_KEY;
  if (!baseURL || !publishableKey) {
    return [];
  }
  const response = await fetch(`${baseURL}/rest/v1/products?select=id,name,category,price_paise,stock,trust_score&order=name.asc`, {
    headers: {
      apikey: publishableKey,
      Authorization: `Bearer ${publishableKey}`,
    },
    next: { revalidate: 30 },
  });
  if (!response.ok) {
    return [];
  }
  return response.json();
}

function formatRupees(paise: number): string {
  return `₹${(paise / 100).toLocaleString("en-IN", { maximumFractionDigits: 0 })}`;
}

export default async function HomePage() {
  const products = await loadProducts();
  return (
    <main className="min-h-screen bg-paper">
      <header className="border-b border-ink/10 bg-ink text-paper">
        <div className="mx-auto flex max-w-7xl items-center justify-between px-6 py-5">
          <div>
            <p className="text-xs font-semibold uppercase tracking-[0.18em] text-mint">Operations</p>
            <h1 className="mt-1 text-2xl font-semibold">AgentMart</h1>
          </div>
          <div className="text-right text-sm text-mint">
            <p>Wallet commerce</p>
            <p className="mt-1 text-xs text-paper/60">Read-only catalog slice</p>
          </div>
        </div>
      </header>

      <div className="mx-auto grid max-w-7xl gap-8 px-6 py-8 lg:grid-cols-[220px_1fr]">
        <aside className="border-r border-ink/10 pr-6">
          <p className="text-xs font-semibold uppercase tracking-[0.18em] text-moss">Workspace</p>
          <nav className="mt-4 space-y-2 text-sm">
            <a className="block border-l-2 border-moss bg-mint px-3 py-2 font-medium text-ink" href="#catalog">Catalog</a>
            <span className="block px-3 py-2 text-ink/45">Wallet</span>
            <span className="block px-3 py-2 text-ink/45">Orders</span>
            <span className="block px-3 py-2 text-ink/45">Revenue</span>
          </nav>
        </aside>

        <section id="catalog" className="min-w-0">
          <div className="flex flex-wrap items-end justify-between gap-4 border-b border-ink/10 pb-5">
            <div>
              <p className="text-sm font-medium text-moss">Merchant catalog</p>
              <h2 className="mt-1 text-3xl font-semibold tracking-tight">Available products</h2>
            </div>
            <div className="text-right text-sm text-ink/60">
              <p>{products.length} seeded products</p>
              <p className="mt-1">Stock and pricing from Supabase</p>
            </div>
          </div>

          {products.length === 0 ? (
            <div className="mt-8 border border-dashed border-ink/20 bg-white p-8 text-sm text-ink/60">
              Catalog data is unavailable. Check the Supabase environment and service status.
            </div>
          ) : (
            <div className="mt-6 grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
              {products.map((product) => (
                <article key={product.id} className="border border-ink/10 bg-white p-5 shadow-sm">
                  <div className="flex items-start justify-between gap-4">
                    <div>
                      <p className="text-xs font-semibold uppercase tracking-[0.14em] text-moss">{product.category}</p>
                      <h3 className="mt-2 text-lg font-semibold">{product.name}</h3>
                    </div>
                    <span className="bg-mint px-2 py-1 text-xs font-semibold text-moss">Trust {product.trust_score}</span>
                  </div>
                  <div className="mt-6 flex items-end justify-between border-t border-ink/10 pt-4">
                    <p className="text-xl font-semibold">{formatRupees(product.price_paise)}</p>
                    <p className="text-sm text-ink/60">{product.stock} in stock</p>
                  </div>
                </article>
              ))}
            </div>
          )}
        </section>
      </div>
    </main>
  );
}
