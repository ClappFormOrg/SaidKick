// @tabler/icons-vue ships one declaration file, for the barrel, and none per
// icon. ~/lib/icons.ts imports icons by their deep path, so without this shim
// each of those imports is a TS7016 under `typescript.strict: true`.
//
// The type is the package's own `Icon`, so these imports type exactly like
// the barrel's.
//
// This file must stay script-scoped: no top-level `import` or `export`. With
// one, `declare module` below becomes a module augmentation instead of an
// ambient pattern declaration and stops matching. That is why the type comes
// in through an inline `import(...)` type query.
declare module '@tabler/icons-vue/dist/esm/icons/*.mjs' {
  const icon: import('@tabler/icons-vue').Icon
  export default icon
}
