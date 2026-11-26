const mods = import.meta.glob('./mods/*.ts', { eager: true });
export const count = Object.keys(mods).length;
