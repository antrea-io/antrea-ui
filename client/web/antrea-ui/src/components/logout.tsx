import { useState } from 'react';
import { useNavigate } from "react-router-dom";
import { authAPI } from '../api/auth';
import { useDispatch } from 'react-redux';
import { setToken } from '../store';

export function useLogout(): [boolean, (() => Promise<void>)] {
    const navigate = useNavigate();
    const [logoutComplete, setLogoutComplete] = useState<boolean>(false);
    const dispatch = useDispatch();

    async function logout() {
        await authAPI.logout();
        dispatch(setToken(""));
        setLogoutComplete(true);
        navigate("/");
        // navigate(0)
    }

    return [logoutComplete, logout];
}
