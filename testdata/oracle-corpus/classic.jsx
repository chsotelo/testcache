export const list = (items) => (
  <ul>
    {items.map((item) => (
      <li key={item.id}>http://item.local/{item.id} /* keep */</li>
    ))}
  </ul>
);
