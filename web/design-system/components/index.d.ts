import type * as React from 'react';
export type IconName = 'pail' | 'plus' | 'terminal' | 'git' | 'upload' | 'folder' | 'copy' | 'check' | 'external' | 'globe' | 'redeploy' | 'trash';
export interface IconProps { name: IconName; size?: number; label?: string; className?: string }
export declare function Icon(props: IconProps): React.ReactElement;
export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> { variant?: 'primary' | 'quiet' | 'ghost' | 'danger'; size?: 'md' | 'sm'; icon?: IconName }
export declare function Button(props: ButtonProps): React.ReactElement;
export type PailState = 'live' | 'building' | 'failed' | 'off';
export interface StatusProps { state: PailState; children?: React.ReactNode }
export declare function Status(props: StatusProps): React.ReactElement;
export interface FieldProps extends React.InputHTMLAttributes<HTMLInputElement> { label?: string; hint?: string; error?: string; prefix?: React.ReactNode; suffix?: React.ReactNode; mono?: boolean }
export declare function Field(props: FieldProps): React.ReactElement;
export interface CommandProps { children: string; comment?: string; className?: string }
export declare function Command(props: CommandProps): React.ReactElement;
export interface Source { id: string; label: string; icon?: IconName; hint?: string }
export interface SourcePickerProps { sources?: Source[]; value?: string | null; defaultValue?: string; onChange?: (id: string) => void; label?: string }
export declare function SourcePicker(props: SourcePickerProps): React.ReactElement;
export interface DropZoneProps { onFiles?: (files: File[]) => void; hint?: string; active?: boolean; progress?: number; fileName?: string }
export declare function DropZone(props: DropZoneProps): React.ReactElement;
export interface PailRowProps { name: string; url: string; href?: string; status: PailState; source: 'cli' | 'upload' | 'github' | 'gitlab' | 'bitbucket' | 'gitea' | 'forgejo' | string; revision?: string; updated?: string; actions?: boolean; onOpen?: () => void; onRedeploy?: () => void; onRemove?: () => void }
export declare function PailRow(props: PailRowProps): React.ReactElement;
export interface LogLine { text: string; time?: string; level?: 'step' | 'ok' | 'error' }
export interface BuildLogProps { lines: Array<LogLine | string> }
export declare function BuildLog(props: BuildLogProps): React.ReactElement;
export interface TopBarProps { host?: string; onNew?: () => void; children?: React.ReactNode }
export declare function TopBar(props: TopBarProps): React.ReactElement;
declare global { interface Window { Pail: { Button: typeof Button; Icon: typeof Icon; Status: typeof Status; Field: typeof Field; Command: typeof Command; SourcePicker: typeof SourcePicker; DropZone: typeof DropZone; PailRow: typeof PailRow; BuildLog: typeof BuildLog; TopBar: typeof TopBar } } }
