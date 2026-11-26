export const greet = (name) => {
  const s = 'hello // not a comment';
  const re = /^hi\s+/i;
  return re.test(name) ? `${s} ${name}` : name;
};
