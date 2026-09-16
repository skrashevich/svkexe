import { hasMultipleUsers } from "./messageAuthors";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

function msgs(...emails: (string | null | undefined)[]) {
  return emails.map((user_email) => ({ user_email }));
}

// Empty conversation: no participants.
assert(hasMultipleUsers([]) === false, "empty -> false");

// Single user, repeated.
assert(
  hasMultipleUsers(msgs("a@x.com", "a@x.com", "a@x.com")) === false,
  "single user repeated -> false",
);

// Two distinct users.
assert(hasMultipleUsers(msgs("a@x.com", "b@x.com")) === true, "two distinct -> true");

// Empty strings are ignored: agent/tool rows and unauthenticated access.
assert(hasMultipleUsers(msgs("", "", "")) === false, "all empty -> false");
assert(hasMultipleUsers(msgs(null, undefined, "")) === false, "nullish/empty -> false");

// Mix of empty and a single real email is still one participant.
assert(
  hasMultipleUsers(msgs("", "a@x.com", "", "a@x.com")) === false,
  "empty + single real email -> false",
);

// Mix of empty and two real emails counts as multiple.
assert(
  hasMultipleUsers(msgs("", "a@x.com", "", "b@x.com")) === true,
  "empty + two real emails -> true",
);

if (failed > 0) {
  console.error(`\n${failed} test(s) failed, ${passed} passed`);
  process.exit(1);
} else {
  console.log(`All ${passed} messageAuthors tests passed`);
}
