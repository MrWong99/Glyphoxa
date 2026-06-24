/* Glyphoxa web UI kit — mock data (fictional campaign content). */

const NPCS = [
  { id: 'n1', name: 'Thornwick the Grey', role: 'Tavern keeper', voice: 'ElevenLabs · Marcus', engine: 'Cascaded', tier: 'Balanced', color: 'var(--speaker-1)' },
  { id: 'n2', name: 'Lady Mireille', role: 'Court enchantress', voice: 'ElevenLabs · Aria', engine: 'S2S — Gemini', tier: 'Deep', color: 'var(--speaker-3)' },
  { id: 'n3', name: 'Khol Ironfist', role: 'Mercenary captain', voice: 'Coqui XTTS · local', engine: 'Cascaded', tier: 'Fast', color: 'var(--speaker-2)' },
  { id: 'n4', name: 'The Hollow King', role: 'Big bad', voice: 'ElevenLabs · Cradle', engine: 'S2S — OpenAI', tier: 'Deep', color: 'var(--speaker-5)' },
];

const CAMPAIGNS = [
  { id: 'c1', name: 'Curse of the Hollow King', system: 'D&D 5e', npcs: 4, sessions: 12, lastPlayed: '2 days ago', live: true },
  { id: 'c2', name: 'Embers of the Ninth Ward', system: 'Blades in the Dark', npcs: 6, sessions: 8, lastPlayed: '1 week ago', live: false },
  { id: 'c3', name: 'The Salt-Wind Reaches', system: 'Pathfinder 2e', npcs: 3, sessions: 21, lastPlayed: '3 weeks ago', live: false },
];

const TRANSCRIPT = [
  { t: '20:11:02', who: 'Game Master', type: 'gm', text: 'You push open the tavern door. Rain drips from your cloak onto warped floorboards.' },
  { t: '20:11:14', who: 'Thornwick the Grey', type: 'npc', npc: 'n1', text: "Shut the door, you're letting the storm in! …Ah. You're not from around here, are you." },
  { t: '20:11:31', who: 'Player — Sora', type: 'player', text: "We're looking for whoever's been buying up the old mill deeds." },
  { t: '20:11:39', who: 'Thornwick the Grey', type: 'npc', npc: 'n1', text: 'Lower your voice. That name buys graves, not drinks. But… the Lady might talk to you.' },
  { t: '20:12:03', who: 'Lady Mireille', type: 'npc', npc: 'n2', text: 'How quaint — visitors with questions. Sit. Let us see what your curiosity is worth.' },
];

const ACTIVITY = [
  { type: 'session', text: 'Session started in Curse of the Hollow King', when: '4 min ago' },
  { type: 'npc', text: 'Lady Mireille joined the voice channel', when: '4 min ago' },
  { type: 'npc', text: 'You edited The Hollow King’s personality', when: '1 hour ago' },
  { type: 'campaign', text: 'New campaign “Embers of the Ninth Ward” created', when: 'Yesterday' },
  { type: 'session', text: 'Session ended · 2h 14m · 318 lines transcribed', when: 'Yesterday' },
];

window.GXData = { NPCS, CAMPAIGNS, TRANSCRIPT, ACTIVITY };
