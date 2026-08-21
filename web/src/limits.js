// The Resource limits form's translation layer: the API speaks millicores and
// "0 skips, -1 clears", the form speaks cores and empty-means-inherited.

// limitInputs renders the stored overrides as form values: an override shows
// its number (cores for CPU), an inherited field shows empty.
export const limitInputs = (overrides) => {
  const ov = overrides || {};
  return {
    memory: ov.memory_mb ? String(ov.memory_mb) : "",
    disk: ov.disk_mb ? String(ov.disk_mb) : "",
    cpu: ov.cpu_milli ? String(ov.cpu_milli / 1000) : "",
  };
};

// limitsPatchBody turns the form back into the PATCH body. An empty field
// means "no override": if one existed it is cleared (-1), else skipped (0).
// A non-numeric value is treated as skip rather than sent; the server floors
// real values anyway.
export const limitsPatchBody = (inputs, overrides) => {
  const ov = overrides || {};
  const field = (value, hadOverride, factor = 1) => {
    const trimmed = String(value ?? "").trim();
    if (trimmed === "") return hadOverride ? -1 : 0;
    const n = Math.round(parseFloat(trimmed) * factor);
    return Number.isFinite(n) && n > 0 ? n : 0;
  };
  return {
    memory_mb: field(inputs.memory, !!ov.memory_mb),
    disk_mb: field(inputs.disk, !!ov.disk_mb),
    cpu_milli: field(inputs.cpu, !!ov.cpu_milli, 1000),
  };
};
