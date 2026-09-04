// What the visibility dialog will actually send, worked out from the list it
// started with and the one on screen. Extracted from the component so it can be
// tested: it decides who loses access, and getting that wrong silently revokes
// people.
//
// `original` is null while the viewer list is still loading. That case produces
// NO removals on purpose -- treating "not loaded yet" as "was empty" turns an
// open-and-save into revoking everybody, which is exactly the bug this shape
// prevents.
export function visibilityChanges(original, people, wasPrivate, isPrivate, wasListed = false, isListed = false) {
  const known = original || [];
  const add = people.filter((p) => !p.id && !known.some((o) => o.email === p.email)).map((p) => p.email);
  const remove = original === null ? [] : known.filter((o) => !people.some((p) => p.email === o.email)).map((o) => o.id);
  return {
    isPrivate,
    listed: isListed,
    add,
    remove,
    changed: isPrivate !== wasPrivate || !!isListed !== !!wasListed || add.length > 0 || remove.length > 0,
  };
}
