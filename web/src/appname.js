// What a person is allowed to type into the app-name box.
//
// The server's rule is ^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$ and the form used to
// let anything be typed, validate on submit, and explain the rule in a line of
// small print underneath. Refusing the keystroke is better on every count: the
// rule needs no explaining, an invalid name is never on screen, and the person
// finds out at the moment they press the key rather than at the moment they
// press the button.

// APP_NAME_MAX is the server's ceiling.
export const APP_NAME_MAX = 32;

// filterAppName drops everything a name may not contain, rather than
// transforming it. Uppercase is DROPPED, not lower-cased: silently rewriting
// what somebody typed is its own surprise, and a letter that simply does not
// appear teaches the rule in one keystroke.
export function filterAppName(value) {
  let out = "";
  for (const ch of value || "") {
    if (out === "") {
      // The first character has to be a letter, so a leading digit or dash is
      // dropped until one arrives.
      if (ch >= "a" && ch <= "z") out = ch;
      continue;
    }
    if ((ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9") || ch === "-") {
      out += ch;
    }
  }
  return out.slice(0, APP_NAME_MAX);
}

// isValidAppName is the SUBMIT rule, which is stricter than the typing rule: a
// name may not END in a dash, but it has to be possible to type "my-app", so a
// trailing dash is allowed while typing and refused at the button.
export function isValidAppName(name) {
  return /^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$/.test(name || "");
}
