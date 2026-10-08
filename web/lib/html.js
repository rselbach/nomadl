// html is the htm tagged template bound to Preact, used instead of JSX so
// the UI runs without a build step.
import { h } from 'preact';
import htm from 'htm';

export const html = htm.bind(h);
