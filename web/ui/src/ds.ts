// The Pail design system, as published: a prebuilt bundle that puts its React
// components on window.Pail and reads React from window when one renders.
import * as React from 'react';
import '../../design-system/tokens.css';
import '../../design-system/components/bundle.css';
import '../../design-system/components/bundle.js';

(window as unknown as { React: typeof React }).React = React;

export const { Button, Icon, Status, Field, Command, SourcePicker, DropZone, PailRow, BuildLog, TopBar } = window.Pail;
export type { IconName, LogLine, PailState, Source } from '../../design-system/components/index';
