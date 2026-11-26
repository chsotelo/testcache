import React from 'react';

export interface Item {
  id: string;
  label: string;
}

export const identity = <T,>(x: T): T => x;
export const fnType: <T>(a: T) => T = (a) => a;

export function Select<T>(props: { items: T[]; value: T }) {
  return <div>{String(props.value)}</div>;
}

export function Card(props: { url: string; count: number; items: Item[] }) {
  const ok = props.count > 0;
  const matched = ok ? /https?:\/\/example\.com/.test(props.url) : false;
  return (
    <div
      className="card"
      data-url="http://attr.example.com/*not-a-comment*/"
      title={'single /* quote */'}
    >
      {/* real jsx comment */}
      <a href={props.url}>http://child.example.com /* literal text */</a>
      <>
        <span>{matched ? 'yes' : 'no'}</span>
        <span>{`nested ${`template ${props.count + 1}`}`}</span>
      </>
      <Select<Item> items={props.items} value={props.items[0]} />
    </div>
  );
}
