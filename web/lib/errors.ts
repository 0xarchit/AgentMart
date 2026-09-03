// Upstream text is for the server log, not for a person. A database error names
// the table, the column and often the constraint it tripped; a gateway error can
// carry an account identifier. Every route below used to hand that straight to
// the browser, which told a shopper nothing they could act on and told anyone
// else reading the response rather more than they should have.

const generic = "Something went wrong on our side. Please try again.";

/**
 * serverFault logs the real fault against the step that hit it and returns the
 * sentence to send back in its place. The fault itself never reaches the caller.
 * The argument is unknown rather than Error because a database error arrives as
 * a plain object carrying details, a hint and a code, none of which survive
 * being read as an Error.
 */
export function serverFault(step: string, fault: unknown): string {
  console.error(`[${step}]`, fault);
  return generic;
}
